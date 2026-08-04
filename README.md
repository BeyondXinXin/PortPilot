# PortPilot

PortPilot 是一个 Windows 桌面端本地服务管理工具，用于启动本地静态文件服务或接管已有 HTTP 服务，并通过 Tailscale Funnel 建立公网入口。

## 功能

- 管理多个静态文件服务和本地代理服务
- 启动前检测端口占用，显示进程名和 PID
- 支持确认关闭占用进程、重新检测和自动关闭策略
- 自动启动/停止 Tailscale Funnel，并动态获取公网地址
- 服务独立启动、停止、重启，支持启动全部和停止全部
- 主界面关闭后驻留系统托盘，退出时执行完整清理
- 自动监测本地代理和 Funnel 状态，异常时尝试恢复
- 配置与运行状态分离，临时公网地址不会写入配置

## 运行要求

PortPilot 本身是单个 Windows 可执行文件，不要求用户安装 Go 或其他开发环境。使用公网暴露前，需要安装并登录 Tailscale，并确保当前 tailnet 已允许 Funnel。

Tailscale Funnel 当前可使用 `443`、`8443` 和 `10000` 三个 HTTPS 入口端口，因此 PortPilot 第一阶段最多同时建立三个公网入口。

## 运行数据目录

```text
C:\Users\<用户名>\AppData\Local\BeyondXinXin\PortPilot\
  config/
    services.json
  logs/
    PortPilot.log
  resources/
    portpilot.ico
    portpilot.png
```

首次运行会自动补齐缺少的目录和配置文件。程序目录只需要保留 `PortPilot.exe`，配置、日志和运行资源不会写到 EXE 所在目录。

## 服务类型

静态文件服务由 PortPilot 内置 HTTP 服务启动：

```text
目录: D:\Share
端口: 8080
本地地址: http://127.0.0.1:8080
```

本地代理服务不会启动目标程序，只检查已有地址并负责公网暴露：

```text
本地地址: http://127.0.0.1:3000
端口: 3000
```

## 配置

配置位于 `%LOCALAPPDATA%\BeyondXinXin\PortPilot\config\services.json`。建议通过界面维护，主要字段如下：

```json
{
  "tailscalePath": "tailscale.exe",
  "services": [
    {
      "id": "svc-example",
      "name": "Website",
      "type": "static",
      "directory": "D:\\Share",
      "localAddress": "http://127.0.0.1:8080",
      "port": 8080,
      "autoStart": true,
      "autoTerminatePortOccupant": false
    }
  ]
}
```

公网地址、Tunnel 端口、运行状态和错误信息只保存在运行时。

## 构建

安装 Go 1.26 或更高版本后运行：

```bat
build.cmd
```

如果 `go.exe` 不在 `PATH`，可以先指定：

```bat
set GO_EXE=C:\Go\bin\go.exe
build.cmd
```

生成单文件便携发布目录：

```bat
release.cmd
```

发布结果位于 `dist\PortPilot\PortPilot.exe`。首次运行后，配置、日志和托盘图标副本会写入 `%LOCALAPPDATA%\BeyondXinXin\PortPilot`。

## 工程结构

```text
cmd/portpilot       程序入口
cmd/makeicon        图标资源生成
internal/app        应用装配
internal/ui         Windows 桌面界面和托盘
internal/config     持久配置
internal/manager    服务生命周期编排
internal/localserver 静态 HTTP 服务和代理端点检查
internal/portcheck  端口、PID 和进程管理
internal/tunnel     Tailscale Funnel 管理
internal/runlog     运行日志
internal/winutil    Windows 系统调用辅助
```

`assets/portpilot.exe.manifest` 会在构建时与图标一起嵌入 EXE，用于启用 Windows Common Controls v6 和高 DPI 支持。
