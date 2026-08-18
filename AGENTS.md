# BeyondXinXin Go 项目开发规范

本文件定义 BeyondXinXin Go 仓库的默认开发约定。

优先遵循当前仓库已经存在的实现和约定；只有仓库没有明确规则时，才使用本文件。

## 基本原则

* 优先简单、直接、可维护的实现。
* 不为完成当前任务而顺手重构无关代码。
* 优先复用现有 package、命名、依赖、配置格式和实现模式。
* 标准库或现有依赖能够解决时，不新增依赖。
* 不为了“以后可能扩展”提前增加 interface、factory、layer 或复杂抽象。
* 保持修改范围与任务直接相关。
* 尽量兼容已有用户配置和已有行为。
* 不在 README、UI、注释或 Release Notes 中描述尚未实现的功能。

进行较大修改前，先检查：

* `go.mod`
* 当前目录结构
* `README.md`
* `.gitignore`
* `build.cmd` / packaging scripts
* `.github/workflows/`

## Go

已有项目始终以 `go.mod` 声明的 Go 版本为准。

新项目默认使用：

```text
Go 1.26
```

GitHub Actions 优先直接读取 `go.mod`：

```yaml
uses: actions/setup-go@v6
with:
  go-version-file: go.mod
```

不要在多个位置重复维护 Go 版本。

修改过的 Go 文件必须执行：

```text
gofmt
```

## 目录结构

Go 桌面程序默认采用：

```text
.
├─ cmd/
│  └─ <app>/
├─ internal/
├─ assets/
├─ resources/
├─ scripts/
├─ build.cmd
├─ go.mod
├─ go.sum
└─ README.md
```

约定：

* `cmd/<app>`：程序入口，只负责初始化、组装和启动。
* `internal/`：业务逻辑和内部实现。
* `internal/<domain>`：按真实职责拆分，不机械套用分层架构。
* `assets/`：图标、manifest 等源码资源。
* `resources/`：程序资源或需要打包、释放的 payload。
* `scripts/`：较复杂的构建或打包辅助脚本。
* 根目录保持简洁，不堆放正常业务源码。

不要为了形式完整而创建没有实际职责的目录或 package。

## Windows 本地数据

Windows 桌面程序的可变数据统一放在：

```text
%LOCALAPPDATA%\BeyondXinXin\<AppName>\
```

按实际需要建立子目录，例如：

```text
config\
logs\
resources\
backups\
runtime\
```

以下内容默认不得写入 EXE 所在目录：

* 用户配置
* API Key / Token
* 日志
* 缓存
* 下载资源
* runtime state
* backup
* 数据库或其他用户数据

Windows 下优先使用：

```go
os.Getenv("LOCALAPPDATA")
```

再拼接：

```text
BeyondXinXin\<AppName>
```

仅在 `LOCALAPPDATA` 不可用时使用合理 fallback。

Linux、Docker、Server 项目遵循对应平台约定，不强行套用 Windows 路径规则。

## 配置

保持当前项目已有的 JSON、YAML 或其他配置格式，不因为统一风格而无意义迁移。

加载配置时优先遵循：

```text
读取
→ 补默认值
→ normalize
→ validate
→ 必要时迁移旧字段
```

新增字段应尽量提供向后兼容的默认值。

普通版本升级不应要求用户删除整个配置目录。

重要配置写回时优先使用原子写入：

```text
config.tmp
→ 写入成功
→ rename
→ config
```

不得提交：

* 真实 API Key
* Token
* Credential
* 本机配置
* 用户数据

## build.cmd

Windows 桌面项目根目录保留：

```text
build.cmd
```

它是本地开发的主要构建入口。

正常情况下：

```cmd
build.cmd
```

应直接完成构建，不要求额外人工准备。

默认流程：

1. 优先使用显式指定的 `GO_EXE`。
2. 未指定时使用 `go`。
3. 构建前结束正在运行的同名 EXE。
4. 清理可能残留的 `<AppName>.exe~`。
5. 必要时生成 icon、manifest、`.syso` 等 Windows resource。
6. 编译程序。
7. 任一步失败立即终止。
8. 失败返回非零 exit code。
9. 成功后明确输出生成文件位置。

结束旧进程通常使用：

```cmd
taskkill /F /IM AppName.exe >nul 2>nul
```

Windows GUI 程序默认使用：

```text
-trimpath
-H windowsgui
-s
-w
```

典型构建：

```cmd
go build -trimpath -ldflags="-H windowsgui -s -w" -o AppName.exe .\cmd\app
```

如果需要 version metadata，通过现有 `ldflags` 追加，不要覆盖原参数。

`build.cmd` 不得要求用户永久修改系统 `PATH`。

可以自动检测本机工具路径，但个人机器上的绝对路径只能作为可选 fallback，不能成为 CI 或其他机器构建的必要条件。

## 本地构建产物

小型 Windows 桌面程序默认直接在仓库根目录生成：

```text
<AppName>.exe
```

优先做到：

```text
build.cmd
→ <AppName>.exe
```

不要无必要增加：

```text
bin/debug/windows/amd64/...
```

这类复杂本地输出结构。

生成的 EXE 不进入 Git。

`.gitignore` 至少应覆盖适用项：

```gitignore
<AppName>.exe
<AppName>.exe~
dist/
*.log
*.test
*.out
```

如果 `.syso` 每次构建都会重新生成，也应忽略：

```gitignore
*.syso
```

## Windows 桌面程序

小型 Windows 工具默认目标：

```text
下载 EXE
→ 双击
→ 使用
```

优先：

* 单文件。
* 免安装。
* 不要求 Administrator。
* 不要求永久修改 `PATH`。
* 不修改不必要的系统设置。
* 不向程序目录写运行数据。
* 尽量不要求额外 runtime。
* 能自动完成的初始化不要交给用户手工操作。

如果程序启动：

* child process
* HTTP server
* proxy
* tunnel
* temporary service

则程序应明确负责对应生命周期。

用户明确退出程序时，应尽量清理自己创建的后台进程和临时 runtime state。

## 网络默认值

本地 HTTP / API 服务在没有明确远程访问需求时默认监听：

```text
127.0.0.1
```

不要无理由改成：

```text
0.0.0.0
```

修改可远程访问的功能时，要保持现有安全边界，重点检查：

* authentication
* Token
* TLS / HTTPS
* Origin
* Host
* WebSocket
* listen address
* port exposure

不要为了方便默默扩大网络暴露范围。

## 版本管理

正式版本使用 Git tag：

```text
v1.0.0
v1.1.0
v1.1.1
```

Git tag 是正式 Release version 的唯一权威来源。

不要同时在多个文件中手工维护同一个正式版本号。

开发版本优先从 Git 获取，例如：

```text
git describe --tags --always --dirty
```

无法获取时 fallback：

```text
dev
```

程序需要展示版本时，优先使用 `-ldflags -X` 注入。

例如：

```go
package version

var Version = "dev"
```

发布时注入：

```text
-X <module>/internal/version.Version=v1.2.3
```

需要诊断信息的项目可以同时注入：

* `Version`
* `GitCommit`
* `BuildTime`

不要通过修改源码才能发布一个新版本。

## GitHub Actions / Release

正式 Release 默认由 Git tag 触发：

```yaml
on:
  push:
    tags:
      - "v*.*.*"
```

可以提供：

```yaml
workflow_dispatch:
```

用于手工构建或验证。

除非项目已有明确需求，手动触发默认不创建正式 Release。

标准发布流程优先为：

```text
checkout
→ setup-go from go.mod
→ go test ./...
→ build/package
→ SHA256
→ GitHub Release
```

如果版本计算依赖 Git tag 或 `git describe`，使用完整历史：

```yaml
with:
  fetch-depth: 0
```

Windows 单平台程序的 Release 文件优先保持简单：

```text
AppName.exe
AppName-v1.2.3.sha256
```

跨平台程序再增加 platform / architecture：

```text
app-windows-amd64.exe
app-linux-amd64
app-linux-arm64
```

默认使用 GitHub 自动生成 Release Notes，除非项目确实需要手写内容。

普通 default branch push 不创建正式 Release。

## README

README 默认使用简体中文。

技术名词、协议、命令、代码、字段名和产品正式名称保持英文，例如：

```text
Go
Tailscale
IPv6
WebSocket
API
Provider
GitHub Actions
```

README 面向使用者，而不是用来解释内部代码架构。

优先快速说明：

```text
这是什么
→ 解决什么问题
→ 为什么值得用
→ 怎么开始
```

根据项目需要使用以下章节，不要求全部存在：

```text
简介
特点
适用场景
快速开始
使用说明
配置 / 数据位置
注意事项
FAQ
License
```

特点优先描述用户获得的能力，例如：

```text
单文件
免安装
自动处理
支持多服务
数据保存在本地
退出自动清理
```

除非实现细节本身就是项目卖点，否则不要把 README 写成技术实现说明。

README 中的：

* command
* path
* port
* config name
* default behavior
* supported feature

必须和当前代码一致。

用户可见行为发生变化时，检查 README 是否需要同步修改。

删除或修改功能时，及时删除已经失效的 README 内容。

不要保留大量无实际价值的：

* badge
* 营销文案
* 重复介绍
* 开发过程记录
* 尚未实现的未来规划

## 依赖

依赖选择优先级：

```text
Go standard library
→ 当前项目已有 dependency
→ 新 dependency
```

只有新依赖能明显降低复杂度或解决真实问题时才引入。

不要为了简单的：

* string processing
* HTTP request
* file operation
* path handling
* small data transformation

引入大型 dependency。

修改依赖后检查 `go.mod` 和 `go.sum`，确保没有无关变化。

## 完成前验证

Go 代码修改完成后，至少执行适用项：

```text
gofmt
go test ./...
```

Windows 桌面项目继续执行：

```text
build.cmd
```

其他项目使用仓库已有的：

```text
package.sh
package.bat
docker build
```

或对应验证流程。

最后检查 Git diff，确认没有误提交：

* `.exe`
* `.exe~`
* log
* credential
* API Key
* user config
* temporary file
* debug code
* build cache
* 无关格式化
* 无关 dependency 修改

同时确认 README 与实际行为一致。

如果当前环境无法执行某个验证步骤，明确说明未验证，不要声称已经通过。

## 决策优先级

出现规则冲突或实现不明确时，按以下顺序决定：

```text
用户当前明确要求
↓
当前仓库已有实现
↓
当前仓库 README / build / workflow 等项目约定
↓
本 AGENTS.md
↓
最简单且可维护的实现
```

不要为了理论上的通用性、扩展性或架构完整度，增加当前项目并不需要的复杂度。

默认目标是：

```text
容易理解
容易修改
容易构建
容易发布
依赖尽量少
系统污染尽量少
已有配置保持稳定
```
