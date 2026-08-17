package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const FileName = "services.json"

const (
	vendorName = "BeyondXinXin"
	appName    = "PortPilot"
)

type ServiceType string
type AccessMode string

const (
	ServiceStatic       ServiceType = "static"
	ServiceProxy        ServiceType = "proxy"
	ServiceBridgeServer ServiceType = "bridge-server"
	ServiceBridgeClient ServiceType = "bridge-client"
)

const (
	AccessAuto            AccessMode = "auto"
	AccessIPv6Direct      AccessMode = "ipv6-direct"
	AccessTailscaleDirect AccessMode = "tailscale-direct"
	AccessTailscaleServe  AccessMode = "tailscale-serve"
	AccessFunnel          AccessMode = "funnel"
	AccessRemoteBridge    AccessMode = "remote-bridge"
)

type Service struct {
	ID                string      `json:"id"`
	Name              string      `json:"name"`
	Type              ServiceType `json:"type"`
	Directory         string      `json:"directory,omitempty"`
	LocalAddress      string      `json:"localAddress"`
	Port              int         `json:"port"`
	AccessMode        AccessMode  `json:"accessMode"`
	AutoStart         bool        `json:"autoStart"`
	AutoTerminatePort bool        `json:"autoTerminatePortOccupant"`
	BridgeListenIP    string      `json:"bridgeListenIP,omitempty"`
	BridgeListenPort  int         `json:"bridgeListenPort,omitempty"`
	BridgeRemoteAddr  string      `json:"bridgeRemoteAddress,omitempty"`
	BridgePairToken   string      `json:"bridgePairToken,omitempty"`
	BridgeLaneCount   int         `json:"bridgeLaneCount,omitempty"`
	BridgeChunkSize   int         `json:"bridgeChunkSize,omitempty"`
}

type Config struct {
	TailscalePath string    `json:"tailscalePath"`
	Services      []Service `json:"services"`
}

func Defaults() Config {
	return Config{TailscalePath: "tailscale.exe", Services: []Service{}}
}

func Directory(fallbackDir string) string {
	if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
		return filepath.Join(localAppData, vendorName, appName)
	}
	return fallbackDir
}

func Paths(baseDir string) (configDir, logDir, resourceDir string) {
	root := Directory(baseDir)
	return filepath.Join(root, "config"), filepath.Join(root, "logs"), filepath.Join(root, "resources")
}

func Ensure(baseDir string) (string, error) {
	configDir, logDir, resourceDir := Paths(baseDir)
	for _, dir := range []string{configDir, logDir, resourceDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", err
		}
	}
	path := filepath.Join(configDir, FileName)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := Save(baseDir, Defaults()); err != nil {
		return "", err
	}
	return path, nil
}

func Load(baseDir string) (Config, error) {
	path, err := Ensure(baseDir)
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	cfg := Defaults()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("读取配置失败: %w", err)
	}
	if strings.TrimSpace(cfg.TailscalePath) == "" {
		cfg.TailscalePath = Defaults().TailscalePath
	}
	seen := make(map[string]struct{}, len(cfg.Services))
	for i := range cfg.Services {
		cfg.Services[i] = NormalizeService(cfg.Services[i])
		if err := ValidateService(cfg.Services[i]); err != nil {
			return Config{}, fmt.Errorf("服务 %q 配置无效: %w", cfg.Services[i].Name, err)
		}
		if _, exists := seen[cfg.Services[i].ID]; exists {
			return Config{}, fmt.Errorf("服务 ID 重复: %s", cfg.Services[i].ID)
		}
		seen[cfg.Services[i].ID] = struct{}{}
	}
	return cfg, nil
}

func Save(baseDir string, cfg Config) error {
	configDir, logDir, resourceDir := Paths(baseDir)
	for _, dir := range []string{configDir, logDir, resourceDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\r', '\n')
	path := filepath.Join(configDir, FileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(path)
		if retryErr := os.Rename(tmp, path); retryErr != nil {
			_ = os.Remove(tmp)
			return retryErr
		}
	}
	return nil
}

func NormalizeService(service Service) Service {
	service.Name = strings.TrimSpace(service.Name)
	service.Directory = strings.TrimSpace(service.Directory)
	service.LocalAddress = strings.TrimSpace(service.LocalAddress)
	service.BridgeListenIP = strings.TrimSpace(service.BridgeListenIP)
	service.BridgeRemoteAddr = strings.TrimSpace(service.BridgeRemoteAddr)
	if service.ID == "" {
		service.ID = NewID()
	}
	if service.AccessMode == "" {
		service.AccessMode = AccessAuto
	}
	if service.Type == ServiceStatic {
		service.LocalAddress = fmt.Sprintf("http://127.0.0.1:%d", service.Port)
	}
	if service.Type == ServiceBridgeClient {
		service.LocalAddress = fmt.Sprintf("http://127.0.0.1:%d", service.Port)
		service.AccessMode = AccessRemoteBridge
	}
	if service.Type == ServiceBridgeServer {
		// Migrate the early explicit Server type to the user-facing Access Mode.
		service.Type = ServiceProxy
		service.AccessMode = AccessRemoteBridge
	}
	if service.AccessMode == AccessRemoteBridge && service.Type != ServiceBridgeClient {
		service.AccessMode = AccessRemoteBridge
		if service.BridgePairToken == "" {
			service.BridgePairToken = NewPairToken()
		}
	}
	if service.AccessMode == AccessRemoteBridge || service.Type == ServiceBridgeClient {
		if service.BridgeLaneCount <= 0 {
			service.BridgeLaneCount = 8
		}
		if service.BridgeChunkSize <= 0 {
			service.BridgeChunkSize = 64 * 1024
		}
		if service.BridgeListenPort <= 0 {
			service.BridgeListenPort = 39090
		}
	}
	return service
}

func ValidateService(service Service) error {
	if strings.TrimSpace(service.Name) == "" {
		return errors.New("名称不能为空")
	}
	if service.Type != ServiceStatic && service.Type != ServiceProxy && service.Type != ServiceBridgeServer && service.Type != ServiceBridgeClient {
		return errors.New("服务类型必须是 static、proxy、bridge-server 或 bridge-client")
	}
	if service.Port < 1 || service.Port > 65535 {
		return errors.New("端口必须在 1 到 65535 之间")
	}
	switch service.AccessMode {
	case AccessAuto, AccessIPv6Direct, AccessTailscaleDirect, AccessTailscaleServe, AccessFunnel, AccessRemoteBridge:
	default:
		return errors.New("访问模式无效")
	}
	if service.Type == ServiceStatic {
		if strings.TrimSpace(service.Directory) == "" {
			return errors.New("静态文件服务必须选择目录")
		}
		return nil
	}
	parsed, err := url.Parse(service.LocalAddress)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("本地地址必须是完整的 http:// 或 https:// 地址")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("本地地址只支持 http 或 https")
	}
	if service.Type == ServiceBridgeServer {
		if net.ParseIP(service.BridgeListenIP) == nil {
			return errors.New("Remote Bridge Server 必须填写监听 Tailscale IP")
		}
		if len(service.BridgePairToken) < 32 {
			return errors.New("Remote Bridge Server 配对 Token 无效")
		}
	}
	if service.Type == ServiceBridgeClient {
		if _, _, err := net.SplitHostPort(service.BridgeRemoteAddr); err != nil {
			return errors.New("Remote Bridge Client 远端地址必须是 IP:端口")
		}
		if len(service.BridgePairToken) < 32 {
			return errors.New("Remote Bridge Client 必须填写配对 Token")
		}
	}
	if service.AccessMode == AccessRemoteBridge || service.Type == ServiceBridgeClient {
		if service.BridgeLaneCount < 1 || service.BridgeLaneCount > 32 {
			return errors.New("Remote Bridge Lane 数量必须在 1 到 32 之间")
		}
		if service.BridgeChunkSize < 4*1024 || service.BridgeChunkSize > 4*1024*1024 {
			return errors.New("Remote Bridge Chunk Size 必须在 4 KB 到 4 MB 之间")
		}
	}
	return nil
}

// NewPairToken returns a 256-bit pairing secret without creating an import cycle
// between configuration and the bridge transport implementation.
func NewPairToken() string {
	var random [32]byte
	if _, err := rand.Read(random[:]); err == nil {
		return hex.EncodeToString(random[:])
	}
	return NewID() + NewID()
}

func NewID() string {
	var random [4]byte
	if _, err := rand.Read(random[:]); err == nil {
		return fmt.Sprintf("svc-%d-%s", time.Now().UnixMilli(), hex.EncodeToString(random[:]))
	}
	return fmt.Sprintf("svc-%d", time.Now().UnixNano())
}
