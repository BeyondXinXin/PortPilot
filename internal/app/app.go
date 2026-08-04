package app

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/BeyondXinXin/portpilot/internal/config"
	"github.com/BeyondXinXin/portpilot/internal/manager"
	"github.com/BeyondXinXin/portpilot/internal/runlog"
	"github.com/BeyondXinXin/portpilot/internal/tunnel"
	"github.com/BeyondXinXin/portpilot/internal/ui"
	"github.com/lxn/walk"
)

//go:embed portpilot.ico
var iconData []byte

func Main() {
	runtime.LockOSThread()
	executableDirectory := executableDir()
	dataDirectory := config.Directory(executableDirectory)
	if _, err := config.Ensure(executableDirectory); err != nil {
		walk.MsgBox(nil, "PortPilot", "创建运行目录失败：\r\n"+err.Error(), walk.MsgBoxIconError)
		return
	}
	if err := ui.EnsureResourceIcon(dataDirectory, iconData); err != nil {
		walk.MsgBox(nil, "PortPilot", "写入资源失败：\r\n"+err.Error(), walk.MsgBoxIconError)
		return
	}
	cfg, err := config.Load(executableDirectory)
	if err != nil {
		walk.MsgBox(nil, "PortPilot", err.Error(), walk.MsgBoxIconError)
		return
	}
	logger, err := runlog.Open(dataDirectory)
	if err != nil {
		walk.MsgBox(nil, "PortPilot", "打开日志失败：\r\n"+err.Error(), walk.MsgBoxIconError)
		return
	}
	defer logger.Close()
	logger.Printf("PortPilot 启动")

	tunnelManager := tunnel.New(cfg.TailscalePath)
	serviceManager := manager.New(cfg.Services, tunnelManager, logger)
	serviceManager.Prepare()
	monitorContext, stopMonitor := context.WithCancel(context.Background())
	defer stopMonitor()
	go serviceManager.Monitor(monitorContext, 10*time.Second)
	mainWindow, err := ui.NewMainWindow(dataDirectory, cfg, serviceManager, logger)
	if err != nil {
		walk.MsgBox(nil, "PortPilot", fmt.Sprintf("创建主界面失败：\r\n%v", err), walk.MsgBoxIconError)
		_ = serviceManager.Shutdown()
		return
	}
	go func() {
		failures := serviceManager.AutoStart()
		for _, failure := range failures {
			logger.Printf("自动启动失败: %v", failure)
		}
	}()
	mainWindow.Run()
	stopMonitor()
	if err := serviceManager.Shutdown(); err != nil {
		logger.Printf("消息循环结束后的清理失败: %v", err)
	}
	logger.Printf("PortPilot 已退出")
}

func executableDir() string {
	executable, err := os.Executable()
	if err != nil {
		workingDir, _ := os.Getwd()
		return workingDir
	}
	return filepath.Dir(executable)
}
