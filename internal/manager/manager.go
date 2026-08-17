package manager

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BeyondXinXin/portpilot/internal/bridge"
	"github.com/BeyondXinXin/portpilot/internal/config"
	"github.com/BeyondXinXin/portpilot/internal/direct"
	"github.com/BeyondXinXin/portpilot/internal/localserver"
	"github.com/BeyondXinXin/portpilot/internal/networkinfo"
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
	Service        config.Service
	Status         Status
	PublicURL      string
	PairingCode    string
	AccessMode     config.AccessMode
	NetworkWarning string
	TunnelPort     int
	LastError      string
	PortOwner      portcheck.Info
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
	opMu           sync.Mutex
	service        config.Service
	status         Status
	publicURL      string
	pairingCode    string
	tunnelPort     int
	lastError      string
	portOwner      portcheck.Info
	localServer    *localserver.Server
	directServer   *direct.Server
	bridgeServer   *bridge.Server
	bridgeClient   *bridge.Client
	accessMode     config.AccessMode
	networkWarning string
}

type Manager struct {
	mu          sync.RWMutex
	runtimes    map[string]*runtime
	order       []string
	tunnel      tunnelController
	logger      *runlog.Logger
	subscribers map[chan Snapshot]struct{}
	closing     atomic.Bool
	funnelUsed  atomic.Bool
	recovering  sync.Map
	networkInfo networkinfo.Info
	networkErr  error
}

type tunnelController interface {
	Reset() error
	Start(serviceID, target string) (tunnel.Assignment, error)
	Stop(serviceID string) error
	Healthy(serviceID string) error
	StartServe(serviceID, target string) (tunnel.Assignment, error)
	StopServe(serviceID string) error
	HealthyServe(serviceID string) error
}

func New(services []config.Service, tunnelManager tunnelController, logger *runlog.Logger) *Manager {
	manager := &Manager{
		runtimes:    make(map[string]*runtime, len(services)),
		tunnel:      tunnelManager,
		logger:      logger,
		subscribers: make(map[chan Snapshot]struct{}),
	}
	manager.networkInfo, manager.networkErr = networkinfo.Detect()
	manager.logNetworkInfo()
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
			if state.status == StatusStopped {
				m.applyCandidateLocked(state)
			}
			continue
		}
		state := &runtime{service: service, status: StatusStopped}
		m.applyCandidateLocked(state)
		m.runtimes[service.ID] = state
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
	} else if service.Type == config.ServiceProxy {
		m.logger.Servicef(id, "检查本地代理服务 %s", service.LocalAddress)
		if err = localserver.CheckEndpoint(service.LocalAddress, 3*time.Second); err != nil {
			return m.fail(state, err)
		}
	}

	m.refreshNetwork()
	access, err := m.startAccess(id, service)
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
	state.directServer = access.directServer
	state.bridgeServer = access.bridgeServer
	state.bridgeClient = access.bridgeClient
	state.publicURL = access.url
	state.pairingCode = access.pairingCode
	state.accessMode = access.mode
	state.networkWarning = access.warning
	state.tunnelPort = access.tunnelPort
	state.status = StatusRunning
	state.portOwner = portcheck.Info{}
	m.mu.Unlock()
	m.logger.Servicef(id, "服务运行成功: %s (%s)", access.url, AccessModeLabel(access.mode))
	if access.warning != "" {
		m.logger.Servicef(id, "网络警告: %s", access.warning)
	}
	m.emit(id)
	return nil
}

type accessStart struct {
	mode         config.AccessMode
	url          string
	warning      string
	tunnelPort   int
	directServer *direct.Server
	bridgeServer *bridge.Server
	bridgeClient *bridge.Client
	pairingCode  string
}

func (m *Manager) startAccess(serviceID string, service config.Service) (accessStart, error) {
	if service.AccessMode == config.AccessRemoteBridge && service.Type != config.ServiceBridgeClient {
		m.mu.RLock()
		address, available := m.networkInfo.PrimaryTailscale()
		m.mu.RUnlock()
		if !available {
			return accessStart{}, errors.New("Remote Bridge 需要本机已连接 Tailscale，并且具有 100.x 地址")
		}
		listen := net.JoinHostPort(address.IP.String(), fmt.Sprintf("%d", service.BridgeListenPort))
		instance, err := bridge.StartServer(bridge.ServerConfig{
			ListenAddress: listen, TargetURL: service.LocalAddress, PairToken: service.BridgePairToken,
			LaneCount: service.BridgeLaneCount, ChunkSize: service.BridgeChunkSize,
		})
		if err != nil {
			return accessStart{}, err
		}
		m.logger.Servicef(serviceID, "Remote Bridge Server 已监听 %s，目标 %s，数据 Lane %d", listen, service.LocalAddress, service.BridgeLaneCount)
		return accessStart{mode: config.AccessRemoteBridge, url: "Remote Bridge Server 已就绪（点击复制访问获取配对码）", pairingCode: bridge.PairingCode(listen, service.BridgePairToken, service.BridgeLaneCount), bridgeServer: instance}, nil
	}
	if service.Type == config.ServiceBridgeClient {
		listen := net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", service.Port))
		instance, err := bridge.StartClient(bridge.ClientConfig{
			ListenAddress: listen, RemoteAddress: service.BridgeRemoteAddr, PairToken: service.BridgePairToken,
			LaneCount: service.BridgeLaneCount, ChunkSize: service.BridgeChunkSize,
		})
		if err != nil {
			return accessStart{}, err
		}
		m.logger.Servicef(serviceID, "Remote Bridge Client 本地入口 %s，远端 %s，数据 Lane %d", "http://"+listen, service.BridgeRemoteAddr, service.BridgeLaneCount)
		return accessStart{mode: config.AccessRemoteBridge, url: "http://" + listen, bridgeClient: instance}, nil
	}
	m.mu.RLock()
	networkState := m.networkInfo
	networkErr := m.networkErr
	m.mu.RUnlock()
	mode, address, available := selectAccessMode(service.AccessMode, networkState)
	if !available {
		if networkErr != nil {
			return accessStart{}, networkErr
		}
		switch service.AccessMode {
		case config.AccessIPv6Direct:
			return accessStart{}, errors.New("未检测到可用的公网 IPv6 地址")
		case config.AccessTailscaleDirect:
			return accessStart{}, errors.New("未检测到 Tailscale 100.x 地址")
		default:
			return accessStart{}, errors.New("没有可用的访问模式")
		}
	}

	switch mode {
	case config.AccessIPv6Direct, config.AccessTailscaleDirect:
		accessURL := directURL(address.IP, service.Port)
		m.logger.Servicef(serviceID, "Network Mode: %s selected", AccessModeLabel(mode))
		m.logger.Servicef(serviceID, "Address: %s", address.IP.String())
		m.logger.Servicef(serviceID, "Port: %d", service.Port)
		m.logger.Servicef(serviceID, "Target: %s", service.LocalAddress)
		directServer, err := direct.Start(address.IP, service.Port, service.LocalAddress)
		if err != nil {
			return accessStart{}, err
		}
		warning := directWarning(mode, service.Port)
		return accessStart{mode: mode, url: accessURL, warning: warning, directServer: directServer}, nil
	case config.AccessTailscaleServe:
		m.logger.Servicef(serviceID, "Network Mode: Tailscale Serve selected")
		serveTarget := fmt.Sprintf("http://127.0.0.1:%d", service.Port)
		var directServer *direct.Server
		if service.Type == config.ServiceProxy {
			m.logger.Servicef(serviceID, "Harness compatible proxy: %s -> %s", serveTarget, service.LocalAddress)
			startedProxy, startErr := direct.Start(net.ParseIP("127.0.0.1"), service.Port, service.LocalAddress)
			if startErr != nil {
				return accessStart{}, startErr
			}
			directServer = startedProxy
		}
		assignment, serveErr := m.tunnel.StartServe(serviceID, serveTarget)
		if serveErr != nil {
			if directServer != nil {
				_ = directServer.Stop(context.Background())
			}
			return accessStart{}, serveErr
		}
		return accessStart{mode: mode, url: assignment.PublicURL, tunnelPort: assignment.HTTPSPort, directServer: directServer}, nil
	default:
		if service.AccessMode == config.AccessAuto {
			m.logger.Servicef(serviceID, "Fallback: Tailscale Funnel")
		}
		m.logger.Servicef(serviceID, "Network Mode: Tailscale Funnel selected")
		assignment, err := m.tunnel.Start(serviceID, service.LocalAddress)
		if err != nil {
			return accessStart{}, err
		}
		m.funnelUsed.Store(true)
		return accessStart{mode: config.AccessFunnel, url: assignment.PublicURL, tunnelPort: assignment.HTTPSPort}, nil
	}
}

func selectAccessMode(requested config.AccessMode, info networkinfo.Info) (config.AccessMode, networkinfo.Address, bool) {
	if requested == "" {
		requested = config.AccessAuto
	}
	switch requested {
	case config.AccessIPv6Direct:
		address, ok := info.PrimaryIPv6()
		return config.AccessIPv6Direct, address, ok
	case config.AccessTailscaleDirect:
		address, ok := info.PrimaryTailscale()
		return config.AccessTailscaleDirect, address, ok
	case config.AccessTailscaleServe:
		return config.AccessTailscaleServe, networkinfo.Address{}, true
	case config.AccessFunnel:
		return config.AccessFunnel, networkinfo.Address{}, true
	default:
		if address, ok := info.PrimaryIPv6(); ok {
			return config.AccessIPv6Direct, address, true
		}
		if address, ok := info.PrimaryTailscale(); ok {
			return config.AccessTailscaleDirect, address, true
		}
		return config.AccessFunnel, networkinfo.Address{}, true
	}
}

func directURL(ip net.IP, port int) string {
	return "http://" + net.JoinHostPort(ip.String(), fmt.Sprintf("%d", port))
}

func directWarning(mode config.AccessMode, port int) string {
	status, err := networkinfo.CheckInboundTCP(port)
	if mode == config.AccessIPv6Direct {
		if err != nil || status != networkinfo.FirewallAllowed {
			return "IPv6 地址存在，但入站访问可能被 Windows 防火墙或路由器阻止"
		}
		return "IPv6 Direct 已监听；仍需确认路由器 IPv6 入站防火墙允许该端口"
	}
	if err != nil || status != networkinfo.FirewallAllowed {
		return "Tailscale 地址存在，但 Windows 防火墙可能阻止入站访问；连接会优先直连，受限时仍可能使用 DERP"
	}
	return "Tailscale 会优先点对点直连；网络受限时仍可能使用 DERP"
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
	directServer := state.directServer
	bridgeServer := state.bridgeServer
	bridgeClient := state.bridgeClient
	accessMode := state.accessMode
	m.mu.Unlock()
	m.emit(id)
	m.logger.Servicef(id, "停止服务 %s", service.Name)

	var failures []error
	if accessMode == config.AccessFunnel {
		if err := m.tunnel.Stop(id); err != nil {
			failures = append(failures, fmt.Errorf("关闭 Funnel: %w", err))
			m.logger.Servicef(id, "关闭 Funnel 失败: %v", err)
		} else {
			m.logger.Servicef(id, "Funnel 已关闭")
		}
	} else if accessMode == config.AccessTailscaleServe {
		if err := m.tunnel.StopServe(id); err != nil {
			failures = append(failures, fmt.Errorf("关闭 Tailscale Serve: %w", err))
			m.logger.Servicef(id, "关闭 Tailscale Serve 失败: %v", err)
		} else {
			m.logger.Servicef(id, "Tailscale Serve 已关闭")
		}
	}
	if directServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := directServer.Stop(ctx); err != nil {
			failures = append(failures, err)
		}
		cancel()
	}
	if bridgeClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := bridgeClient.Stop(ctx); err != nil {
			failures = append(failures, fmt.Errorf("停止 Remote Bridge Client: %w", err))
		}
		cancel()
	}
	if bridgeServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := bridgeServer.Stop(ctx); err != nil {
			failures = append(failures, fmt.Errorf("停止 Remote Bridge Server: %w", err))
		}
		cancel()
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
	state.directServer = nil
	state.bridgeServer = nil
	state.bridgeClient = nil
	state.pairingCode = ""
	state.networkWarning = ""
	state.tunnelPort = 0
	state.portOwner = portcheck.Info{}
	if len(failures) == 0 {
		state.status = StatusStopped
		state.lastError = ""
		m.applyCandidateLocked(state)
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
	if m.funnelUsed.Load() {
		if err := m.tunnel.Reset(); err != nil {
			failures = append(failures, fmt.Errorf("最终 Tunnel 清理或验证失败: %w", err))
		}
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
				if healthErr == nil && snapshot.Service.AccessMode == config.AccessRemoteBridge && snapshot.Service.Type != config.ServiceBridgeClient {
					healthErr = m.bridgeServerHealthy(snapshot.Service.ID)
				} else if healthErr == nil && snapshot.Service.Type == config.ServiceBridgeClient {
					healthErr = m.bridgeClientHealthy(snapshot.Service.ID)
				}
				if healthErr == nil {
					switch snapshot.AccessMode {
					case config.AccessFunnel:
						healthErr = m.tunnel.Healthy(snapshot.Service.ID)
					case config.AccessTailscaleServe:
						healthErr = m.tunnel.HealthyServe(snapshot.Service.ID)
					case config.AccessRemoteBridge:
						// Remote Bridge has already checked its own listener/client above.
					default:
						healthErr = m.directHealthy(snapshot.Service.ID)
					}
				}
				if healthErr != nil {
					m.scheduleRecovery(snapshot.Service.ID, healthErr)
				}
			}
		}
	}
}

func (m *Manager) directHealthy(id string) error {
	m.mu.RLock()
	state, exists := m.runtimes[id]
	if !exists || state.directServer == nil {
		m.mu.RUnlock()
		return errors.New("Direct listener is not running")
	}
	directServer := state.directServer
	m.mu.RUnlock()
	return directServer.Healthy()
}

func (m *Manager) bridgeServerHealthy(id string) error {
	m.mu.RLock()
	state, exists := m.runtimes[id]
	instance := (*bridge.Server)(nil)
	if exists {
		instance = state.bridgeServer
	}
	m.mu.RUnlock()
	if instance == nil {
		return errors.New("Remote Bridge Server 未运行")
	}
	return instance.Healthy()
}

func (m *Manager) bridgeClientHealthy(id string) error {
	m.mu.RLock()
	state, exists := m.runtimes[id]
	instance := (*bridge.Client)(nil)
	if exists {
		instance = state.bridgeClient
	}
	m.mu.RUnlock()
	if instance == nil {
		return errors.New("Remote Bridge Client 未运行")
	}
	return instance.Healthy()
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

func (m *Manager) refreshNetwork() {
	info, err := networkinfo.Detect()
	m.mu.Lock()
	m.networkInfo = info
	m.networkErr = err
	for _, state := range m.runtimes {
		if state.status == StatusStopped {
			m.applyCandidateLocked(state)
		}
	}
	m.mu.Unlock()
	m.logNetworkInfo()
}

func (m *Manager) logNetworkInfo() {
	if m.logger == nil {
		return
	}
	m.mu.RLock()
	info := m.networkInfo
	err := m.networkErr
	m.mu.RUnlock()
	if err != nil {
		m.logger.Printf("网络地址检测失败: %v", err)
		return
	}
	if address, ok := info.PrimaryIPv6(); ok {
		kind := "稳定/非临时"
		if address.Temporary {
			kind = "临时"
		}
		m.logger.Printf("检测到 IPv6 Direct 地址: %s (%s, %s)", address.IP, address.InterfaceName, kind)
	} else {
		m.logger.Printf("未检测到公网 IPv6 地址")
	}
	if address, ok := info.PrimaryTailscale(); ok {
		m.logger.Printf("检测到 Tailscale Direct 地址: %s (%s)", address.IP, address.InterfaceName)
	} else {
		m.logger.Printf("未检测到 Tailscale 100.x 地址")
	}
}

func (m *Manager) applyCandidateLocked(state *runtime) {
	if state.service.Type == config.ServiceBridgeServer {
		state.accessMode = config.AccessRemoteBridge
		state.networkWarning = "仅监听指定的 Tailscale IP；配对码包含敏感 Token，请妥善保存"
		state.publicURL = "Remote Bridge Server 已就绪（点击复制访问获取配对码）"
		return
	}
	if state.service.AccessMode == config.AccessRemoteBridge {
		state.accessMode = config.AccessRemoteBridge
		state.networkWarning = "启动后点击“复制访问”获取配对码；浏览器永远访问 Client 的 localhost"
		state.publicURL = "Remote Bridge Server（尚未启动）"
		return
	}
	if state.service.Type == config.ServiceBridgeClient {
		state.accessMode = config.AccessRemoteBridge
		state.networkWarning = "浏览器始终访问本机 localhost；远端断开时 Client 会自动重连"
		state.publicURL = state.service.LocalAddress
		return
	}
	mode, address, available := selectAccessMode(state.service.AccessMode, m.networkInfo)
	state.accessMode = mode
	state.networkWarning = ""
	state.publicURL = ""
	if available && (mode == config.AccessIPv6Direct || mode == config.AccessTailscaleDirect) {
		state.publicURL = directURL(address.IP, state.service.Port)
	}
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
		PairingCode: state.pairingCode,
		AccessMode:  state.accessMode, NetworkWarning: state.networkWarning,
		TunnelPort: state.tunnelPort, LastError: state.lastError, PortOwner: state.portOwner,
	}
}

func AccessModeLabel(mode config.AccessMode) string {
	switch mode {
	case config.AccessIPv6Direct:
		return "IPv6 Direct"
	case config.AccessTailscaleDirect:
		return "Tailscale Direct"
	case config.AccessTailscaleServe:
		return "Tailscale Serve"
	case config.AccessFunnel:
		return "Tailscale Funnel"
	case config.AccessRemoteBridge:
		return "Remote Bridge"
	default:
		return "Auto"
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
