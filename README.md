# PortPilot

> 用一个轻量 Windows 桌面工具，把本地网站和 Web 服务提供给需要访问的设备。

PortPilot 用来管理两类服务：静态网站目录，或已经运行的 HTTP / HTTPS 服务。无论服务只监听 `localhost`，还是你手头只有一个网站目录，都可以从一个界面选择合适的入口：公网 IPv6、Tailnet 直连、Tailscale HTTPS、公网 Funnel，或 Remote Bridge。

它解决的是“本机能打开，但手机、另一台电脑或互联网用户该怎么访问”的问题，不要求你重写服务配置或搭建一套复杂的反向代理。

---

## 为什么值得用

- **一个界面管理多个服务**：查看本地地址、访问地址、模式和状态，按服务启动、停止、重启或复制地址。
- **不改动原服务**：静态网站由 PortPilot 启动；已有服务只需填入完整的 HTTP / HTTPS 地址。
- **按网络边界选择入口**：私有 Tailnet、直接 IPv6、公网临时分享和“浏览器必须是 `localhost`”各有对应模式。
- **照顾 `localhost` Web 应用**：转发到本机目标时，会将 `Host` 和已有的 `Origin` 改回目标本地地址；HTTP 与 `WebSocket` 都支持。
- **日常使用简单**：关闭窗口后仍驻留托盘；运行中会定期检查入口，异常时尝试恢复。退出时停止已建立的入口。

---

## 五种模式怎么选

| 模式 | 它在做什么 | Tailscale | 公网 IPv6 | HTTP / HTTPS | 公网可访问 | 性能与最适合的场景 |
| --- | --- | --- | --- | --- | --- | --- |
| **IPv6 Direct** | 在本机公网 IPv6 地址上反向代理服务，地址如 `http://[IPv6]:端口` | 不需要 | 需要 | HTTP | 可以 | 路径最直接，适合已能控制路由器与防火墙入站规则的公开站点 |
| **Tailscale Direct** | 在本机 `100.x.x.x` Tailscale 地址上反向代理服务 | 服务端和访问端都需要 | 不需要 | HTTP | 不可以 | Tailscale 优先设备间直连，受限时可能走 DERP；适合普通 Tailnet 内部工具 |
| **Tailscale Serve** | 通过 Tailscale `serve` 提供 Tailnet 内 HTTPS 地址 | 服务端和访问端都需要 | 不需要 | 浏览器 HTTPS | 不可以 | 无需自行配证书；适合需要 HTTPS 或浏览器安全上下文的 Tailnet 服务 |
| **Tailscale Funnel** | 通过 Tailscale `funnel` 提供互联网 HTTPS 地址 | 服务端需要，且 Tailnet 必须允许 Funnel | 不需要 | 浏览器 HTTPS | 可以 | 便利优先于直连；适合临时演示和外网分享 |
| **Remote Bridge** | 让电脑 B 的 `localhost` 通过 Tailnet 转发到电脑 A 的服务 | 两端都需要 | 不需要 | 浏览器访问本机 HTTP `localhost` | 不可以 | 多一段桥接传输，换取 `localhost` 兼容性；适合 DeepSeek Harness 等本机来源敏感服务 |

**最短决策：**

- 有稳定公网 IPv6 且希望直连：IPv6 Direct。
- 只在自己的 Tailnet 使用：普通网页选 Tailscale Direct；需要 HTTPS 选 Tailscale Serve。
- 要临时开放到互联网：Tailscale Funnel。
- 远端浏览器也必须像访问本机服务一样访问 `localhost`：Remote Bridge。

> IPv6 Direct 和 Funnel 会把服务交给互联网。PortPilot 不会额外为目标应用加入登录、鉴权或限流；公开前请确认服务本身适合公开。

---

## DeepSeek Harness：推荐 Remote Bridge

[DeepSeekHarnessBox](https://github.com/BeyondXinXin/deepseek-harness-box) 会将 DeepSeek Harness 运行在本机 `127.0.0.1`，默认端口为 `3081`。两个项目没有强制依赖：Harness 可独立使用，PortPilot 也可管理任何本地 Web 服务。

如果 Harness 在电脑 A 上运行、但你希望在电脑 B 上使用，优先使用 **Remote Bridge**：

~~~text
电脑 B 的浏览器
  http://127.0.0.1:13081
          │
          ▼
PortPilot Bridge Client ── Tailscale ── PortPilot Bridge Server
                                            │
                                            ▼
                         Harness: 127.0.0.1:3081（电脑 A）
~~~

这样做的关键不是单纯“把端口转出去”，而是：

- Harness 在 A 上仍只监听 `localhost`，不需要改监听地址；
- B 的浏览器始终访问 B 自己的 `localhost`，适合要求本机来源或对 `Host` / `Origin` 敏感的 Web UI；
- PortPilot 会转发普通 HTTP 和 `WebSocket`；面向 A 的 `localhost` 目标时，会将后端看到的 `Host` 和已有 `Origin` 恢复为 A 的本地 Harness 地址；
- 桥接只在两台 Tailnet 设备之间工作，不产生公网入口。

如果需求只是“让 Tailnet 其他设备通过一个 HTTPS URL 打开 Harness”，也可以选择 Tailscale Serve；Remote Bridge 则是希望远端使用体验保持为本机 `localhost` 时的推荐方案。

### 配置步骤

1. 在电脑 A 启动 DeepSeekHarnessBox，确认 Harness 实际地址，例如 `http://127.0.0.1:3081`。
2. 在 A 的 PortPilot 添加“本地代理服务”，填入该地址，Access Mode 选择 **Remote Bridge**，然后启动。
3. 右键 A 上的服务，选择“复制访问”以复制配对码。
4. 在电脑 B 的 PortPilot 点击“添加服务”，类型选择 **Remote Bridge**，粘贴配对码并启动。
5. 在 B 打开 `http://127.0.0.1:13081`，或你设置的本机入口端口。

配对码含有桥接地址和秘密 Token，应按密码处理，只通过可信渠道发送。两端都要加入同一 Tailnet，并且 ACL 需要允许 B 连接 A 的桥接端口（默认 `39090`）。

---

## 快速开始

1. 从 [GitHub Releases](https://github.com/BeyondXinXin/PortPilot/releases) 下载 Windows x64 单文件版本 `PortPilot-…-windows-amd64.exe`；正式发布附带同名 `.sha256` 校验文件。
2. 运行 PortPilot，点击“添加服务”。
3. 选择一种服务类型：
   - **静态文件服务**：选择网站目录与服务端口；
   - **本地代理服务**：填入已运行服务的完整地址，例如 `http://127.0.0.1:3000`，并设置入口端口。
4. 按上表选择模式，保存后右键服务选择“启动”。
5. 从服务行复制或打开“访问地址”。

使用 Tailscale Direct、Serve、Funnel 或 Remote Bridge 前，请在相关电脑安装并登录 Tailscale。静态服务会在本机端口运行；本地代理服务不会启动你的目标程序，只会检查它是否可连接。

---

## 数据与要求

数据保存在：

```text
%LOCALAPPDATA%\BeyondXinXin\PortPilot\
├─ config\services.json   # 服务、模式、Tailscale CLI 路径与 Bridge 配对 Token
├─ logs\PortPilot.log      # 运行日志
└─ resources\portpilot.ico
```

不要公开 `services.json` 或 Remote Bridge 配对码；其中可能包含访问凭证。

- **系统**：Windows x64；静态网站和本地代理不需要 Node.js、Python 或额外 Web 服务器。
- **Tailscale 模式**：服务端需要安装、登录 Tailscale，并让 PortPilot 调用 Tailscale CLI。默认 CLI 名称为 `tailscale.exe`，可在托盘“设置”中修改，重启后生效。
- **IPv6 Direct**：还需要公网 IPv6，以及 Windows 防火墙和路由器放行目标 TCP 端口。

**Serve/Funnel 地址带 `:8443` 或 `:10000`？** PortPilot 依次使用 `443`、`8443`、`10000` 管理 Tailscale HTTPS 入口；可用端口不是 `443` 时，URL 就会带端口号。

**有公网 IPv6 仍无法访问？** 检查 Windows 入站防火墙、路由器 IPv6 防火墙，以及访问端是否具备 IPv6 连通性。
