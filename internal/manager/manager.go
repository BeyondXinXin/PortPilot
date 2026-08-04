package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BeyondXinXin/portpilot/internal/config"
	"github.com/BeyondXinXin/portpilot/internal/localserver"
	"github.com/BeyondXinXin/portpilot/internal/portcheck"
	"github.com/BeyondXinXin/portpilot/internal/runlog"
	"github.com/BeyondXinXin/portpilot/internal/tunnel"
)

type Status string

const (
	StatusStopped  Status = "stopped"
	StatusStarting Status = "starting"
	StatusRunning  Status = "running"
	StatusStopping Status = "stopping"
	StatusError    Status = "error"
)

type Snapshot struct {
	Service    config.Service
	Status     Status
	PublicURL  string
	TunnelPort int
	LastError  string
	PortOwner  portcheck.Info
}

type ConflictError struct {
	Info portcheck.Info
}

func (e *ConflictError) Error() string {
	process := e.Info.ProcessName
	if process == "" {
		process = "未知进程"
	}
	return fmt.Sprintf("端口 %d 被 %s 占用（PID %d）", e.Info.Port, process, e.Info.PID)
}

type runtime struct {
	opMu        sync.Mutex
	service     config.Service
	status      Status
	publicURL   string
	tunnelPort  int
	lastError   string
	portOwner   portcheck.Info
	localServer *localserver.Server
}

type Manager struct {
	mu          sync.RWMutex
	runtimes    map[string]*runtime
	order       []string
	tunnel      tunnelController
	logger      *runlog.Logger
	subscribers map[chan Snapshot]struct{}
	closing     atomic.Bool
	recovering  sync.Map
}

type tunnelController interface {
	Reset() error
	Start(serviceID, target string) (tunnel.Assignment, error)
	Stop(serviceID string) error
	Healthy(serviceID string) error
}

func New(services []config.Service, tunnelManager tunnelController, logger *runlog.Logger) *Manager {
	manager := &Manager{
		runtimes:    make(map[string]*runtime, len(services)),
		tunnel:      tunnelManager,
		logger:      logger,
		subscribers: make(map[chan Snapshot]struct{}),
	}
	_ = manager.SetServices(services)
	return manager
}

func (m *Manager) Prepare() {
	m.logger.Printf("清理旧 Tunnel 状态")
	if err := m.tunnel.Reset(); err != nil {
		m.logger.Printf("清理旧 Tunnel 状态失败: %v", err)
	}
}

func (m *Manager) Subscribe() (<-chan Snapshot, func()) {
	channel := make(chan Snapshot, 64)
	m.mu.Lock()
	m.subscribers[channel] = struct{}{}
	m.mu.Unlock()
	return channel, func() {
		m.mu.Lock()
		if _, exists := m.subscribers[channel]; exists {
			delete(m.subscribers, channel)
			close(channel)
		}
		m.mu.Unlock()
	}
}

func (m *Manager) Snapshots() []Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]Snapshot, 0, len(m.order))
	for _, id := range m.order {
		if state, exists := m.runtimes[id]; exists {
			result = append(result, snapshotOf(state))
		}
	}
	return result
}

func (m *Manager) Snapshot(id string) (Snapshot, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	state, exists := m.runtimes[id]
	if !exists {
		return Snapshot{}, false
	}
	return snapshotOf(state), true
}

func (m *Manager) SetServices(services []config.Service) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	newIDs := make(map[string]struct{}, len(services))
	newOrder := make([]string, 0, len(services))
	for _, service := range services {
		service = config.NormalizeService(service)
		newIDs[service.ID] = struct{}{}
		newOrder = append(newOrder, service.ID)
		if state, exists := m.runtimes[service.ID]; exists {
			if state.status != StatusStopped && state.service != service {
				return fmt.Errorf("服务 %s 正在运行，停止后才能修改", state.service.Name)
			}
			state.service = service
			continue
		}
		m.runtimes[service.ID] = &runtime{service: service, status: StatusStopped}
	}
	for id, state := range m.runtimes {
		if _, exists := newIDs[id]; exists {
			continue
		}
		if state.status != StatusStopped {
			return fmt.Errorf("服务 %s 正在运行，停止后才能删除", state.service.Name)
		}
		delete(m.runtimes, id)
	}
	m.order = newOrder
	return nil
}

func (m *Manager) Start(id string) error {
	if m.closing.Load() {
		return errors.New("PortPilot 正在退出")
	}
	state, err := m.runtime(id)
	if err != nil {
		return err
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()

	m.mu.Lock()
	if state.status == StatusRunning || state.status == StatusStarting {
		m.mu.Unlock()
		return nil
	}
	state.status = StatusStarting
	state.lastError = ""
	state.portOwner = portcheck.Info{}
	service := state.service
	m.mu.Unlock()
	m.emit(id)
	m.logger.Servicef(id, "启动服务 %s", service.Name)

	var local *localserver.Server
	if service.Type == config.ServiceStatic {
		dirInfo, statErr := os.Stat(service.Directory)
		if statErr != nil {
			return m.fail(state, fmt.Errorf("静态文件目录不可用: %w", statErr))
		}
		if !dirInfo.IsDir() {
			return m.fail(state, errors.New("静态文件路径不是目录"))
		}
		m.logger.Servicef(id, "检测端口 %d", service.Port)
		info, inspectErr := portcheck.Inspect(service.Port)
		if inspectErr != nil {
			return m.fail(state, inspectErr)
		}
		if info.Occupied {
			m.mu.Lock()
			state.portOwner = info
			m.mu.Unlock()
			m.logger.Servicef(id, "端口被占用: %s PID %d", info.ProcessName, info.PID)
			if !service.AutoTerminatePort {
				return m.fail(state, &ConflictError{Info: info})
			}
			m.logger.Servicef(id, "自动关闭端口占用进程 PID %d", info.PID)
			if terminateErr := portcheck.Terminate(info); terminateErr != nil {
				return m.fail(state, terminateErr)
			}
			if _, waitErr := portcheck.WaitUntilFree(service.Port, 5*time.Second); waitErr != nil {
				return m.fail(state, waitErr)
			}
		}
		m.logger.Servicef(id, "启动静态文件服务 %s", service.LocalAddress)
		local, err = localserver.StartStatic(service.Directory, service.Port)
		if err != nil {
			return m.fail(state, err)
		}
	} else {
		m.logger.Servicef(id, "检查本地代理服务 %s", service.LocalAddress)
		if err = localserver.CheckEndpoint(service.LocalAddress, 3*time.Second); err != nil {
			return m.fail(state, err)
		}
	}

	m.logger.Servicef(id, "建立 Tailscale Funnel")
	assignment, err := m.tunnel.Start(id, service.LocalAddress)
	if err != nil {
		if local != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = local.Stop(ctx)
			cancel()
		}
		return m.fail(state, err)
	}

	m.mu.Lock()
	state.localServer = local
	state.publicURL = assignment.PublicURL
	state.tunnelPort = assignment.HTTPSPort
	state.status = StatusRunning
	state.portOwner = portcheck.Info{}
	m.mu.Unlock()
	m.logger.Servicef(id, "服务运行成功: %s", assignment.PublicURL)
	m.emit(id)
	return nil
}

func (m *Manager) Stop(id string) error {
	state, err := m.runtime(id)
	if err != nil {
		return err
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()

	m.mu.Lock()
	if state.status == StatusStopped {
		m.mu.Unlock()
		return nil
	}
	state.status = StatusStopping
	service := state.service
	local := state.localServer
	m.mu.Unlock()
	m.emit(id)
	m.logger.Servicef(id, "停止服务 %s", service.Name)

	var failures []error
	if err := m.tunnel.Stop(id); err != nil {
		failures = append(failures, fmt.Errorf("关闭 Funnel: %w", err))
		m.logger.Servicef(id, "关闭 Funnel 失败: %v", err)
	} else {
		m.logger.Servicef(id, "Funnel 已关闭")
	}
	if local != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := local.Stop(ctx); err != nil {
			failures = append(failures, fmt.Errorf("停止本地服务: %w", err))
		}
		cancel()
	}

	m.mu.Lock()
	state.localServer = nil
	state.publicURL = ""
	state.tunnelPort = 0
	state.portOwner = portcheck.Info{}
	if len(failures) == 0 {
		state.status = StatusStopped
		state.lastError = ""
	} else {
		state.status = StatusError
		state.lastError = errors.Join(failures...).Error()
	}
	m.mu.Unlock()
	m.emit(id)
	if len(failures) != 0 {
		return errors.Join(failures...)
	}
	m.logger.Servicef(id, "服务已停止")
	return nil
}

func (m *Manager) Restart(id string) error {
	if err := m.Stop(id); err != nil {
		return err
	}
	return m.Start(id)
}

func (m *Manager) StartAll() []error {
	var failures []error
	for _, snapshot := range m.Snapshots() {
		if err := m.Start(snapshot.Service.ID); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", snapshot.Service.Name, err))
		}
	}
	return failures
}

func (m *Manager) AutoStart() []error {
	var failures []error
	for _, snapshot := range m.Snapshots() {
		if snapshot.Service.AutoStart {
			if err := m.Start(snapshot.Service.ID); err != nil {
				failures = append(failures, fmt.Errorf("%s: %w", snapshot.Service.Name, err))
			}
		}
	}
	return failures
}

func (m *Manager) StopAll() []error {
	var failures []error
	snapshots := m.Snapshots()
	for i := len(snapshots) - 1; i >= 0; i-- {
		if err := m.Stop(snapshots[i].Service.ID); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", snapshots[i].Service.Name, err))
		}
	}
	return failures
}

func (m *Manager) Shutdown() error {
	m.closing.Store(true)
	m.logger.Printf("执行完整退出清理")
	failures := m.StopAll()
	if err := m.tunnel.Reset(); err != nil {
		failures = append(failures, fmt.Errorf("最终 Tunnel 清理或验证失败: %w", err))
	}
	if len(failures) == 0 {
		m.logger.Printf("退出清理完成")
		return nil
	}
	return errors.Join(failures...)
}

func (m *Manager) Monitor(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if m.closing.Load() {
				return
			}
			for _, snapshot := range m.Snapshots() {
				if snapshot.Status != StatusRunning {
					continue
				}
				var healthErr error
				if snapshot.Service.Type == config.ServiceProxy {
					healthErr = localserver.CheckEndpoint(snapshot.Service.LocalAddress, 2*time.Second)
				}
				if healthErr == nil {
					healthErr = m.tunnel.Healthy(snapshot.Service.ID)
				}
				if healthErr != nil {
					m.scheduleRecovery(snapshot.Service.ID, healthErr)
				}
			}
		}
	}
}

func (m *Manager) scheduleRecovery(id string, cause error) {
	if _, loaded := m.recovering.LoadOrStore(id, struct{}{}); loaded {
		return
	}
	go func() {
		defer m.recovering.Delete(id)
		if m.closing.Load() {
			return
		}
		snapshot, ok := m.Snapshot(id)
		if !ok || snapshot.Status != StatusRunning {
			return
		}
		m.logger.Servicef(id, "检测到服务异常，准备自动恢复: %v", cause)
		if err := m.Restart(id); err != nil {
			m.logger.Servicef(id, "自动恢复失败: %v", err)
		}
	}()
}

func (m *Manager) runtime(id string) (*runtime, error) {
	m.mu.RLock()
	state, exists := m.runtimes[id]
	m.mu.RUnlock()
	if !exists {
		return nil, errors.New("服务不存在")
	}
	return state, nil
}

func (m *Manager) fail(state *runtime, err error) error {
	m.mu.Lock()
	state.status = StatusError
	state.lastError = err.Error()
	id := state.service.ID
	m.mu.Unlock()
	m.logger.Servicef(id, "启动失败: %v", err)
	m.emit(id)
	return err
}

func (m *Manager) emit(id string) {
	m.mu.RLock()
	state, exists := m.runtimes[id]
	if !exists {
		m.mu.RUnlock()
		return
	}
	snapshot := snapshotOf(state)
	subscribers := make([]chan Snapshot, 0, len(m.subscribers))
	for subscriber := range m.subscribers {
		subscribers = append(subscribers, subscriber)
	}
	m.mu.RUnlock()
	for _, subscriber := range subscribers {
		select {
		case subscriber <- snapshot:
		default:
		}
	}
}

func snapshotOf(state *runtime) Snapshot {
	return Snapshot{
		Service: state.service, Status: state.status, PublicURL: state.publicURL,
		TunnelPort: state.tunnelPort, LastError: state.lastError, PortOwner: state.portOwner,
	}
}

func StatusLabel(status Status) string {
	switch status {
	case StatusStarting:
		return "启动中"
	case StatusRunning:
		return "运行中"
	case StatusStopping:
		return "停止中"
	case StatusError:
		return "异常"
	default:
		return "已停止"
	}
}
