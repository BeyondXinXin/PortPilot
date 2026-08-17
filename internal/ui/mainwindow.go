package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/BeyondXinXin/portpilot/internal/config"
	"github.com/BeyondXinXin/portpilot/internal/manager"
	"github.com/BeyondXinXin/portpilot/internal/portcheck"
	"github.com/BeyondXinXin/portpilot/internal/runlog"
	"github.com/BeyondXinXin/portpilot/internal/winutil"
	"github.com/lxn/walk"
)

const WindowTitle = "PortPilot - 本地服务公网管理"

type MainWindow struct {
	baseDir  string
	config   config.Config
	manager  *manager.Manager
	logger   *runlog.Logger
	window   *walk.MainWindow
	table    *walk.TableView
	model    *serviceTableModel
	status   *walk.Label
	tray     *walk.NotifyIcon
	icon     *walk.Icon
	quitting atomic.Bool
	unsub    func()
}

func NewMainWindow(baseDir string, cfg config.Config, serviceManager *manager.Manager, logger *runlog.Logger) (*MainWindow, error) {
	window, err := walk.NewMainWindow()
	if err != nil {
		return nil, err
	}
	ui := &MainWindow{baseDir: baseDir, config: cfg, manager: serviceManager, logger: logger, window: window, model: &serviceTableModel{}}
	if err := ui.build(); err != nil {
		window.Dispose()
		return nil, err
	}
	return ui, nil
}

func (ui *MainWindow) build() error {
	ui.window.SetTitle(WindowTitle)
	ui.window.SetSize(walk.Size{Width: 1040, Height: 620})
	ui.window.SetMinMaxSize(walk.Size{Width: 840, Height: 480}, walk.Size{})
	layout := walk.NewVBoxLayout()
	layout.SetMargins(walk.Margins{HNear: 12, VNear: 12, HFar: 12, VFar: 10})
	layout.SetSpacing(8)
	ui.window.SetLayout(layout)

	toolbar, _ := walk.NewComposite(ui.window)
	toolbarLayout := walk.NewHBoxLayout()
	toolbarLayout.SetMargins(walk.Margins{})
	toolbarLayout.SetSpacing(6)
	toolbar.SetLayout(toolbarLayout)
	ui.addButton(toolbar, "添加", ui.addService)
	ui.addButton(toolbar, "编辑", ui.editSelected)
	ui.addButton(toolbar, "删除", ui.deleteSelected)
	ui.addButton(toolbar, "启动", func() { ui.runSelected("start") })
	ui.addButton(toolbar, "停止", func() { ui.runSelected("stop") })
	ui.addButton(toolbar, "重启", func() { ui.runSelected("restart") })
	ui.addButton(toolbar, "启动全部", ui.startAll)
	ui.addButton(toolbar, "停止全部", ui.stopAll)
	_, _ = walk.NewHSpacer(toolbar)
	ui.addButton(toolbar, "打开本地", func() { ui.openSelected(false) })
	ui.addButton(toolbar, "打开访问", func() { ui.openSelected(true) })
	ui.addButton(toolbar, "复制本地", func() { ui.copySelected(false) })
	ui.addButton(toolbar, "复制访问", func() { ui.copySelected(true) })
	ui.addButton(toolbar, "日志", ui.openLog)
	ui.addButton(toolbar, "设置", ui.settings)

	table, err := walk.NewTableView(ui.window)
	if err != nil {
		return err
	}
	ui.table = table
	table.SetModel(ui.model)
	for _, column := range []struct {
		title string
		width int
	}{{"服务名称", 150}, {"类型", 130}, {"状态", 80}, {"本地地址", 230}, {"访问地址 / 配对码", 300}, {"Access Mode", 120}} {
		viewColumn := walk.NewTableViewColumn()
		viewColumn.SetTitle(column.title)
		viewColumn.SetWidth(column.width)
		if err := table.Columns().Add(viewColumn); err != nil {
			return err
		}
	}
	table.ItemActivated().Attach(ui.editSelected)
	table.CurrentIndexChanged().Attach(ui.updateStatus)

	status, _ := walk.NewLabel(ui.window)
	status.SetText("就绪")
	ui.status = status
	ui.refresh()

	ui.window.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		if !ui.quitting.Load() {
			*canceled = true
			ui.window.Hide()
			ui.tray.ShowInfo("PortPilot", "PortPilot 正在托盘中运行。")
		}
	})
	if err := ui.buildTray(); err != nil {
		return err
	}

	updates, unsubscribe := ui.manager.Subscribe()
	ui.unsub = unsubscribe
	go func() {
		for range updates {
			ui.window.Synchronize(func() { ui.refresh() })
		}
	}()
	return nil
}

func (ui *MainWindow) buildTray() error {
	tray, err := walk.NewNotifyIcon(ui.window)
	if err != nil {
		return err
	}
	ui.tray = tray
	iconPath := filepath.Join(ui.baseDir, "resources", "portpilot.ico")
	if icon, iconErr := walk.NewIconFromFile(iconPath); iconErr == nil {
		ui.icon = icon
		_ = tray.SetIcon(icon)
		_ = ui.window.SetIcon(icon)
	}
	tray.SetToolTip("PortPilot")
	ui.addTrayAction("打开管理界面", ui.show)
	ui.addTrayAction("启动全部服务", ui.startAll)
	ui.addTrayAction("停止全部服务", ui.stopAll)
	ui.addTrayAction("查看日志", ui.openLog)
	separator := walk.NewSeparatorAction()
	_ = tray.ContextMenu().Actions().Add(separator)
	ui.addTrayAction("退出", ui.exit)
	tray.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.LeftButton {
			ui.show()
		}
	})
	return tray.SetVisible(true)
}

func (ui *MainWindow) Run() int {
	ui.window.Show()
	code := ui.window.Run()
	if ui.unsub != nil {
		ui.unsub()
	}
	if ui.tray != nil {
		ui.tray.Dispose()
	}
	if ui.icon != nil {
		ui.icon.Dispose()
	}
	return code
}

func (ui *MainWindow) addButton(parent walk.Container, text string, handler func()) {
	button, _ := walk.NewPushButton(parent)
	button.SetText(text)
	button.Clicked().Attach(handler)
}

func (ui *MainWindow) addTrayAction(text string, handler func()) {
	action := walk.NewAction()
	action.SetText(text)
	action.Triggered().Attach(handler)
	_ = ui.tray.ContextMenu().Actions().Add(action)
}

func (ui *MainWindow) show() {
	ui.window.Show()
	ui.window.BringToTop()
}

func (ui *MainWindow) selected() (manager.Snapshot, bool) {
	return ui.model.item(ui.table.CurrentIndex())
}

func (ui *MainWindow) refresh() {
	selectedID := ""
	if selected, ok := ui.selected(); ok {
		selectedID = selected.Service.ID
	}
	ui.model.setItems(ui.manager.Snapshots())
	if selectedID != "" {
		for index, item := range ui.model.items {
			if item.Service.ID == selectedID {
				ui.table.SetCurrentIndex(index)
				break
			}
		}
	}
	ui.updateStatus()
}

func (ui *MainWindow) updateStatus() {
	selected, ok := ui.selected()
	if !ok {
		ui.status.SetText(fmt.Sprintf("共 %d 个服务", len(ui.model.items)))
		return
	}
	text := fmt.Sprintf("%s | %s | %s | 端口 %d", selected.Service.Name, manager.StatusLabel(selected.Status), selected.Service.LocalAddress, selected.Service.Port)
	if selected.PublicURL != "" {
		text += " | " + selected.PublicURL
	}
	text += " | " + manager.AccessModeLabel(selected.AccessMode)
	if selected.NetworkWarning != "" {
		text += " | " + selected.NetworkWarning
	}
	if selected.LastError != "" {
		text += " | " + selected.LastError
	}
	ui.status.SetText(text)
}

func (ui *MainWindow) addService() {
	service, ok := editService(ui.window, config.Service{ID: config.NewID(), Type: config.ServiceStatic, Port: 8080})
	if !ok {
		return
	}
	services := append(append([]config.Service{}, ui.config.Services...), service)
	ui.applyServices(services)
}

func (ui *MainWindow) editSelected() {
	selected, ok := ui.selected()
	if !ok {
		return
	}
	if selected.Status != manager.StatusStopped {
		walk.MsgBox(ui.window, "无法编辑", "请先停止服务。", walk.MsgBoxIconWarning)
		return
	}
	service, accepted := editService(ui.window, selected.Service)
	if !accepted {
		return
	}
	services := append([]config.Service{}, ui.config.Services...)
	for index := range services {
		if services[index].ID == service.ID {
			services[index] = service
			break
		}
	}
	ui.applyServices(services)
}

func (ui *MainWindow) deleteSelected() {
	selected, ok := ui.selected()
	if !ok {
		return
	}
	if selected.Status != manager.StatusStopped {
		walk.MsgBox(ui.window, "无法删除", "请先停止服务。", walk.MsgBoxIconWarning)
		return
	}
	if walk.MsgBox(ui.window, "删除服务", fmt.Sprintf("确定删除 %s？", selected.Service.Name), walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != int(walk.DlgCmdYes) {
		return
	}
	services := make([]config.Service, 0, len(ui.config.Services)-1)
	for _, service := range ui.config.Services {
		if service.ID != selected.Service.ID {
			services = append(services, service)
		}
	}
	ui.applyServices(services)
}

func (ui *MainWindow) applyServices(services []config.Service) {
	previous := ui.config.Services
	if err := ui.manager.SetServices(services); err != nil {
		walk.MsgBox(ui.window, "配置未保存", err.Error(), walk.MsgBoxIconError)
		return
	}
	ui.config.Services = services
	if err := config.Save(ui.baseDir, ui.config); err != nil {
		ui.config.Services = previous
		_ = ui.manager.SetServices(previous)
		walk.MsgBox(ui.window, "配置未保存", err.Error(), walk.MsgBoxIconError)
		return
	}
	ui.refresh()
}

func (ui *MainWindow) runSelected(operation string) {
	selected, ok := ui.selected()
	if !ok {
		return
	}
	ui.status.SetText("正在执行，请稍候...")
	go func() {
		var err error
		switch operation {
		case "stop":
			err = ui.manager.Stop(selected.Service.ID)
		case "restart":
			err = ui.manager.Restart(selected.Service.ID)
		default:
			err = ui.manager.Start(selected.Service.ID)
		}
		ui.window.Synchronize(func() { ui.handleOperationError(selected.Service.ID, operation, err) })
	}()
}

func (ui *MainWindow) handleOperationError(serviceID, operation string, err error) {
	ui.refresh()
	if err == nil {
		return
	}
	var conflict *manager.ConflictError
	if operation == "start" && errors.As(err, &conflict) {
		message := fmt.Sprintf("端口：%d\r\n占用程序：%s\r\nPID：%d\r\n\r\n是否关闭该进程并重新检测？", conflict.Info.Port, conflict.Info.ProcessName, conflict.Info.PID)
		if walk.MsgBox(ui.window, "端口占用", message, walk.MsgBoxYesNo|walk.MsgBoxIconWarning) == int(walk.DlgCmdYes) {
			go func() {
				terminateErr := portcheck.Terminate(conflict.Info)
				if terminateErr == nil {
					_, terminateErr = portcheck.WaitUntilFree(conflict.Info.Port, 5_000_000_000)
				}
				if terminateErr == nil {
					terminateErr = ui.manager.Start(serviceID)
				}
				ui.window.Synchronize(func() { ui.handleOperationError(serviceID, "retry", terminateErr) })
			}()
		}
		return
	}
	walk.MsgBox(ui.window, "操作失败", err.Error(), walk.MsgBoxIconError)
}

func (ui *MainWindow) startAll() {
	go func() {
		failures := ui.manager.StartAll()
		ui.window.Synchronize(func() { ui.showFailures("启动全部服务", failures) })
	}()
}

func (ui *MainWindow) stopAll() {
	go func() {
		failures := ui.manager.StopAll()
		ui.window.Synchronize(func() { ui.showFailures("停止全部服务", failures) })
	}()
}

func (ui *MainWindow) showFailures(title string, failures []error) {
	ui.refresh()
	if len(failures) == 0 {
		return
	}
	parts := make([]string, 0, len(failures))
	for _, failure := range failures {
		parts = append(parts, failure.Error())
	}
	walk.MsgBox(ui.window, title, strings.Join(parts, "\r\n"), walk.MsgBoxIconError)
}

func (ui *MainWindow) openSelected(public bool) {
	selected, ok := ui.selected()
	if !ok {
		return
	}
	target := selected.Service.LocalAddress
	if public {
		if selected.Service.AccessMode == config.AccessRemoteBridge && selected.Service.Type != config.ServiceBridgeClient {
			walk.MsgBox(ui.window, "配对码", "Remote Bridge Server 的“访问地址”是配对码，请使用“复制访问”发送到 Client，而不是在浏览器打开。", walk.MsgBoxIconInformation)
			return
		}
		target = selected.PublicURL
	}
	if target == "" {
		walk.MsgBox(ui.window, "地址不可用", "服务尚未获得访问地址。", walk.MsgBoxIconWarning)
		return
	}
	if err := winutil.Open(target); err != nil {
		walk.MsgBox(ui.window, "打开失败", err.Error(), walk.MsgBoxIconError)
	}
}

func (ui *MainWindow) copySelected(public bool) {
	selected, ok := ui.selected()
	if !ok {
		return
	}
	target := selected.Service.LocalAddress
	label := "本地地址"
	if public {
		target = selected.PublicURL
		label = "访问地址"
		if selected.Service.AccessMode == config.AccessRemoteBridge && selected.Service.Type != config.ServiceBridgeClient {
			label = "Remote Bridge 配对码（含敏感 Token）"
			target = selected.PairingCode
			if target == "" {
				walk.MsgBox(ui.window, "配对码不可用", "请先启动 Remote Bridge Server，再复制配对码。", walk.MsgBoxIconWarning)
				return
			}
		}
	}
	if target == "" {
		walk.MsgBox(ui.window, "地址不可用", "服务尚未获得访问地址。", walk.MsgBoxIconWarning)
		return
	}
	if err := winutil.CopyText(target); err != nil {
		walk.MsgBox(ui.window, "复制失败", err.Error(), walk.MsgBoxIconError)
		return
	}
	ui.status.SetText(label + "已复制：" + target)
	if selected.Service.AccessMode == config.AccessRemoteBridge && selected.Service.Type != config.ServiceBridgeClient && public {
		walk.MsgBox(ui.window, "配对码已复制", "已复制到剪贴板。请到另一台电脑的 PortPilot 中点击“添加”，选择 Remote Bridge 并粘贴该配对码。", walk.MsgBoxIconInformation)
	}
}

func (ui *MainWindow) openLog() {
	if err := winutil.Open(ui.logger.Path); err != nil {
		walk.MsgBox(ui.window, "打开日志失败", err.Error(), walk.MsgBoxIconError)
	}
}

func (ui *MainWindow) settings() {
	path, ok := editSettings(ui.window, ui.config.TailscalePath)
	if !ok || path == ui.config.TailscalePath {
		return
	}
	if running := ui.runningCount(); running != 0 {
		walk.MsgBox(ui.window, "无法修改", "请先停止全部服务，再修改 Tailscale CLI 路径。", walk.MsgBoxIconWarning)
		return
	}
	ui.config.TailscalePath = path
	if err := config.Save(ui.baseDir, ui.config); err != nil {
		walk.MsgBox(ui.window, "配置未保存", err.Error(), walk.MsgBoxIconError)
		return
	}
	walk.MsgBox(ui.window, "设置已保存", "Tailscale CLI 路径将在下次启动 PortPilot 时生效。", walk.MsgBoxIconInformation)
}

func (ui *MainWindow) runningCount() int {
	count := 0
	for _, item := range ui.manager.Snapshots() {
		if item.Status == manager.StatusRunning || item.Status == manager.StatusStarting {
			count++
		}
	}
	return count
}

func (ui *MainWindow) exit() {
	if ui.quitting.Swap(true) {
		return
	}
	ui.status.SetText("正在停止服务并撤销公网暴露...")
	ui.show()
	go func() {
		err := ui.manager.Shutdown()
		ui.window.Synchronize(func() {
			if err != nil {
				ui.quitting.Store(false)
				walk.MsgBox(ui.window, "退出清理失败", err.Error(), walk.MsgBoxIconError)
				return
			}
			if ui.tray != nil {
				_ = ui.tray.SetVisible(false)
			}
			ui.window.Close()
		})
	}()
}

func EnsureResourceIcon(baseDir string, data []byte) error {
	path := filepath.Join(baseDir, "resources", "portpilot.ico")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
