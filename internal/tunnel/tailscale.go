package tunnel

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/BeyondXinXin/portpilot/internal/winutil"
)

var publicURLPattern = regexp.MustCompile(`https://[a-zA-Z0-9.-]+\.ts\.net(?::\d+)?(?:/[^\s]*)?`)

type Assignment struct {
	ServiceID string
	HTTPSPort int
	PublicURL string
}

type Manager struct {
	executable string
	mu         sync.Mutex
	active     map[string]Assignment
}

func New(executable string) *Manager {
	return &Manager{executable: resolveExecutable(executable), active: make(map[string]Assignment)}
}

func (m *Manager) Available() error {
	if _, err := os.Stat(m.executable); err == nil {
		return nil
	}
	if _, err := exec.LookPath(m.executable); err != nil {
		return fmt.Errorf("未找到 Tailscale CLI: %s", m.executable)
	}
	return nil
}

func (m *Manager) Reset() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.Available(); err != nil {
		return err
	}
	if _, err := m.run("funnel", "reset"); err != nil {
		return err
	}
	var lastStatus string
	for attempt := 0; attempt < 6; attempt++ {
		status, err := m.run("funnel", "status", "--json")
		if err != nil {
			return fmt.Errorf("验证 Funnel 清理状态失败: %w", err)
		}
		lastStatus = status
		if noFunnelConfig(status) {
			m.active = make(map[string]Assignment)
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("Funnel reset 后仍存在公网配置: %s", lastStatus)
}

func (m *Manager) Start(serviceID, target string) (Assignment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.active[serviceID]; ok {
		return existing, nil
	}
	if err := m.Available(); err != nil {
		return Assignment{}, err
	}
	port, err := m.availablePort()
	if err != nil {
		return Assignment{}, err
	}
	output, err := m.run("funnel", "--bg", fmt.Sprintf("--https=%d", port), target)
	if err != nil {
		return Assignment{}, err
	}
	publicURL := extractPublicURL(output)
	if publicURL == "" {
		for attempt := 0; attempt < 5 && publicURL == ""; attempt++ {
			time.Sleep(250 * time.Millisecond)
			status, statusErr := m.run("funnel", "status")
			if statusErr == nil {
				publicURL = extractURLForPort(status, port)
			}
		}
	}
	if publicURL == "" {
		_ = m.stopPort(port)
		return Assignment{}, errors.New("Funnel 已启动，但未能获取公网地址")
	}
	assignment := Assignment{ServiceID: serviceID, HTTPSPort: port, PublicURL: strings.TrimRight(publicURL, "/")}
	m.active[serviceID] = assignment
	return assignment, nil
}

func (m *Manager) Stop(serviceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	assignment, ok := m.active[serviceID]
	if !ok {
		return nil
	}
	err := m.stopPort(assignment.HTTPSPort)
	if err == nil {
		delete(m.active, serviceID)
	}
	return err
}

func (m *Manager) Healthy(serviceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	assignment, ok := m.active[serviceID]
	if !ok {
		return errors.New("服务没有活动的 Funnel")
	}
	status, err := m.run("funnel", "status")
	if err != nil {
		return err
	}
	publicURL := strings.TrimRight(assignment.PublicURL, "/")
	if !strings.Contains(status, publicURL) {
		return fmt.Errorf("Funnel 状态中未找到 %s", publicURL)
	}
	return nil
}

func (m *Manager) stopPort(port int) error {
	_, err := m.run("funnel", fmt.Sprintf("--https=%d", port), "off")
	return err
}

func (m *Manager) availablePort() (int, error) {
	ports := []int{443, 8443, 10000}
	used := make(map[int]struct{}, len(m.active))
	for _, assignment := range m.active {
		used[assignment.HTTPSPort] = struct{}{}
	}
	for _, port := range ports {
		if _, exists := used[port]; !exists {
			return port, nil
		}
	}
	return 0, errors.New("Tailscale Funnel 最多同时使用 3 个 HTTPS 入口端口")
}

func (m *Manager) run(args ...string) (string, error) {
	cmd := exec.Command(m.executable, args...)
	winutil.HideWindow(cmd)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		if text == "" {
			return "", err
		}
		return text, fmt.Errorf("tailscale %s 失败: %w: %s", strings.Join(args, " "), err, text)
	}
	return text, nil
}

func extractPublicURL(output string) string {
	return publicURLPattern.FindString(output)
}

func extractURLForPort(output string, port int) string {
	for _, line := range strings.Split(output, "\n") {
		if port != 443 && !strings.Contains(line, fmt.Sprintf(":%d", port)) {
			continue
		}
		if match := publicURLPattern.FindString(line); match != "" {
			return match
		}
	}
	return extractPublicURL(output)
}

func noFunnelConfig(output string) bool {
	var status map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &status); err != nil {
		return false
	}
	return len(status) == 0
}

func resolveExecutable(configured string) string {
	configured = strings.TrimSpace(configured)
	if configured != "" && !strings.EqualFold(configured, "tailscale.exe") {
		return configured
	}
	if path, err := exec.LookPath("tailscale.exe"); err == nil {
		return path
	}
	candidate := filepath.Join(os.Getenv("ProgramFiles"), "Tailscale", "tailscale.exe")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return "tailscale.exe"
}
