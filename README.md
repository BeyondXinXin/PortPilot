# PortPilot

PortPilot 是 Windows 上的本地服务发布工具。它可以启动静态网站，或把已有的本地 HTTP 服务安全地提供给局域网、Tailnet 或公网访问。

## 特点

- 一个桌面应用管理多个静态网站和本地代理服务
- 自动检测端口占用，可提示或自动关闭占用进程
- 服务可单独启动、停止、重启，并支持自动启动与状态监测
- 支持 IPv6 Direct、Tailscale Direct、Tailscale Serve 和 Tailscale Funnel
- 关闭主窗口后继续驻留系统托盘；退出时自动清理访问入口
- 配置、日志和图标资源自动保存在用户数据目录，不污染程序目录

## 适用场景

- 在自己的公网 IPv6 网络上快速发布静态网站
- 让 Tailnet 中的设备访问开发机上的 Web 服务
- 为本地服务生成 Tailscale 的 HTTPS 地址，无需自己配置证书
- 将网站临时通过 Tailscale Funnel 公开到互联网
- 将 DeepSeek Harness 等只信任本机来源的服务安全地分享给 Tailnet

## 添加服务

在 PortPilot 中点击“添加服务”。

- **静态文件服务**：选择网站目录和端口，PortPilot 会启动文件服务。
- **本地代理服务**：填写已经运行的本地地址，例如 `http://127.0.0.1:3000`；PortPilot 不会启动目标程序，只负责访问入口。

## 访问方式

| 方式 | 适合什么情况 | 访问地址 |
| --- | --- | --- |
| Auto | 不想手动选择，优先直连 | 自动选择 IPv6 Direct、Tailscale Direct 或 Funnel |
| IPv6 Direct | 有可用公网 IPv6 | `http://[IPv6]:端口` |
| Tailscale Direct | Tailnet 内普通 HTTP 服务 | `http://100.x.x.x:端口` |
| Tailscale Serve | Tailnet 内需要 HTTPS 的服务 | `https://设备名.<tailnet>.ts.net` |
| Tailscale Funnel | 需要公开给互联网 | Tailscale 分配的 HTTPS 地址 |

使用 Tailscale 的方式需要安装并登录 Tailscale。Serve 会自动使用 Tailscale 的 HTTPS 证书；Funnel 需要 Tailnet 已允许 Funnel。

## DeepSeek Harness

Harness Web UI 需要 HTTPS 才能使用浏览器的 `crypto.randomUUID()`。请使用 **Tailscale Serve - Tailnet HTTPS（Harness 推荐）**，不要使用 `http://100.x.x.x:端口`。

示例：

```text
Harness:          http://127.0.0.1:3081
PortPilot 端口:    8088
访问地址:          https://<设备>.<tailnet>.ts.net
```

PortPilot 会先在 `127.0.0.1:8088` 建立代理，再由 Tailscale Serve 转发到它。转发给 Harness 时，会把 `Host` 和已有的 `Origin` 重写为 Harness 的本地地址；普通 HTTP 和 WebSocket 都适用。

## 数据位置

配置和日志位于：

```text
%LOCALAPPDATA%\BeyondXinXin\PortPilot\
```
