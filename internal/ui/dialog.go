package ui

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/BeyondXinXin/portpilot/internal/bridge"
	"github.com/BeyondXinXin/portpilot/internal/config"
	"github.com/lxn/walk"
)

func newService(owner walk.Form, initial config.Service) (config.Service, bool) {
	return serviceDialog(owner, initial, false, serviceDialogActions{})
}

func editService(owner walk.Form, initial config.Service, actions serviceDialogActions) (config.Service, bool) {
	return serviceDialog(owner, initial, false, actions)
}

type serviceDialogActions struct {
	openLocal              func()
	openAccess             func()
	copyAccessLabel        string
	copyAccess             func()
	accessActionsVisible   bool
	serviceActionLabel     string
	serviceActionNextLabel string
	serviceAction          func(func(error))
	serviceActionNext      func(func(error))
}

func viewService(owner walk.Form, initial config.Service, actions serviceDialogActions) {
	_, _ = serviceDialog(owner, initial, true, actions)
}

func serviceDialog(owner walk.Form, initial config.Service, readOnly bool, actions serviceDialogActions) (config.Service, bool) {
	dialog, err := walk.NewDialog(owner)
	if err != nil {
		return config.Service{}, false
	}
	defer dialog.Dispose()
	if readOnly {
		dialog.SetTitle("服务配置（只读）")
	} else {
		dialog.SetTitle("服务配置")
	}
	layout := walk.NewVBoxLayout()
	layout.SetMargins(walk.Margins{HNear: 14, VNear: 14, HFar: 14, VFar: 14})
	layout.SetSpacing(10)
	dialog.SetLayout(layout)

	form, _ := walk.NewComposite(dialog)
	formLayout := walk.NewVBoxLayout()
	formLayout.SetMargins(walk.Margins{})
	formLayout.SetSpacing(8)
	_ = formLayout.SetAlignment(walk.AlignHNearVNear)
	form.SetLayout(formLayout)

	typeCombo := addDialogCombo(form, "类型", []string{"Remote Bridge", "静态文件服务", "本地代理服务"})
	switch initial.Type {
	case config.ServiceBridgeClient:
		typeCombo.SetCurrentIndex(0)
	case config.ServiceProxy, config.ServiceBridgeServer:
		typeCombo.SetCurrentIndex(2)
	default:
		typeCombo.SetCurrentIndex(1)
	}
	nameEdit := addDialogLine(form, "名称", initial.Name)

	staticSection := newDialogSection(form)
	directoryRow := addDialogRow(staticSection, "目录")
	directoryEdit, _ := walk.NewLineEdit(directoryRow)
	directoryEdit.SetText(initial.Directory)
	directoryRow.Layout().(*walk.BoxLayout).SetStretchFactor(directoryEdit, 1)
	browseButton, _ := walk.NewPushButton(directoryRow)
	browseButton.SetText("浏览...")
	browseButton.Clicked().Attach(func() {
		fileDialog := new(walk.FileDialog)
		fileDialog.Title = "选择静态文件目录"
		if ok, browseErr := fileDialog.ShowBrowseFolder(dialog); browseErr == nil && ok {
			directoryEdit.SetText(fileDialog.FilePath)
		}
	})

	staticPortLabel, staticPort := addDialogLabeledLine(staticSection, "服务端口", servicePort(initial, 8080))
	staticTerminate := addDialogCheck(staticSection, "自动关闭端口占用进程", initial.AutoTerminatePort)

	proxySection := newDialogSection(form)
	addressEdit := addDialogLine(proxySection, "本地地址", initial.LocalAddress)
	proxyPortLabel, proxyPort := addDialogLabeledLine(proxySection, "代理入口端口", servicePort(initial, 8080))
	proxyTerminate := addDialogCheck(proxySection, "自动关闭端口占用进程", initial.AutoTerminatePort)

	bridgeSection := newDialogSection(form)
	bridgePortLabel, bridgePort := addDialogLabeledLine(bridgeSection, "本机入口端口", servicePort(initial, 13081))
	pairingCode := addDialogLine(bridgeSection, "配对码", "")

	autoStart := addDialogCheck(form, "自动启动", initial.AutoStart)
	accessGroup, _ := walk.NewGroupBox(form)
	accessGroup.SetTitle("Access Mode")
	accessLayout := walk.NewVBoxLayout()
	accessLayout.SetMargins(walk.Margins{HNear: 8, VNear: 6, HFar: 8, VFar: 6})
	accessLayout.SetSpacing(4)
	_ = accessLayout.SetAlignment(walk.AlignHNearVCenter)
	accessGroup.SetLayout(accessLayout)
	ipv6Access, _ := walk.NewRadioButton(accessGroup)
	ipv6Access.SetText("IPv6 Direct - 最快，公网直连")
	tailscaleAccess, _ := walk.NewRadioButton(accessGroup)
	tailscaleAccess.SetText("Tailscale Direct - Tailnet 私网")
	tailscaleServeAccess, _ := walk.NewRadioButton(accessGroup)
	tailscaleServeAccess.SetText("Tailscale Serve - Tailnet HTTPS")
	funnelAccess, _ := walk.NewRadioButton(accessGroup)
	funnelAccess.SetText("Tailscale Funnel - 公网中继")
	bridgeAccess, _ := walk.NewRadioButton(accessGroup)
	bridgeAccess.SetText("Remote Bridge - Harness 推荐，本机 localhost")
	switch initial.AccessMode {
	case config.AccessIPv6Direct:
		ipv6Access.SetChecked(true)
	case config.AccessTailscaleDirect:
		tailscaleAccess.SetChecked(true)
	case config.AccessTailscaleServe:
		tailscaleServeAccess.SetChecked(true)
	case config.AccessFunnel:
		funnelAccess.SetChecked(true)
	case config.AccessRemoteBridge:
		bridgeAccess.SetChecked(true)
	default:
		tailscaleAccess.SetChecked(true)
	}

	updateType := func() {
		isBridge := typeCombo.CurrentIndex() == 0
		isStatic := typeCombo.CurrentIndex() == 1
		staticPortLabel.SetText("服务端口")
		proxyPortLabel.SetText("代理入口端口")
		bridgePortLabel.SetText("本机入口端口")
		bridgeSection.SetVisible(isBridge)
		staticSection.SetVisible(isStatic)
		proxySection.SetVisible(!isBridge && !isStatic)
		accessGroup.SetVisible(!isBridge)
		if isBridge && initial.Type != config.ServiceBridgeClient && strings.TrimSpace(bridgePort.Text()) == "8080" {
			bridgePort.SetText("13081")
		}
	}
	typeCombo.CurrentIndexChanged().Attach(updateType)
	updateType()
	_, _ = walk.NewVSpacer(dialog)
	buttons, _ := walk.NewComposite(dialog)
	buttonLayout := walk.NewHBoxLayout()
	buttonLayout.SetMargins(walk.Margins{})
	buttonLayout.SetSpacing(8)
	buttons.SetLayout(buttonLayout)
	var setReadOnly func(bool)
	accessActionButtons := make([]*walk.PushButton, 0, 3)
	if actions.openLocal != nil {
		accessActionButtons = append(accessActionButtons, addServiceDialogAction(buttons, "打开本地", actions.openLocal))
	}
	if actions.openAccess != nil {
		accessActionButtons = append(accessActionButtons, addServiceDialogAction(buttons, "打开访问", actions.openAccess))
	}
	if actions.copyAccess != nil {
		accessActionButtons = append(accessActionButtons, addServiceDialogAction(buttons, actions.copyAccessLabel, actions.copyAccess))
	}
	setAccessActionsVisible := func(visible bool) {
		for _, button := range accessActionButtons {
			button.SetVisible(visible)
		}
	}
	var serviceActionButton *walk.PushButton
	if actions.serviceAction != nil {
		currentAction := actions.serviceAction
		nextAction := actions.serviceActionNext
		currentLabel := actions.serviceActionLabel
		nextLabel := actions.serviceActionNextLabel
		serviceActionButton = addServiceDialogAction(buttons, actions.serviceActionLabel, func() {
			serviceActionButton.SetEnabled(false)
			currentAction(func(operationErr error) {
				serviceActionButton.SetEnabled(true)
				if operationErr != nil {
					return
				}
				currentAction, nextAction = nextAction, currentAction
				currentLabel, nextLabel = nextLabel, currentLabel
				serviceActionButton.SetText(currentLabel)
				setReadOnly(currentLabel == "停止")
				setAccessActionsVisible(currentLabel == "停止")
			})
		})
	}
	spacer, _ := walk.NewHSpacer(buttons)
	_ = spacer
	saveButton, _ := walk.NewPushButton(buttons)
	saveButton.SetText("保存")
	cancelButton, _ := walk.NewPushButton(buttons)
	cancelButton.Clicked().Attach(dialog.Cancel)
	setReadOnly = func(value bool) {
		typeCombo.SetEnabled(!value)
		nameEdit.SetReadOnly(value)
		directoryEdit.SetReadOnly(value)
		browseButton.SetEnabled(!value)
		staticPort.SetReadOnly(value)
		staticTerminate.SetEnabled(!value)
		addressEdit.SetReadOnly(value)
		proxyPort.SetReadOnly(value)
		proxyTerminate.SetEnabled(!value)
		bridgePort.SetReadOnly(value)
		pairingCode.SetReadOnly(value)
		autoStart.SetEnabled(!value)
		ipv6Access.SetEnabled(!value)
		tailscaleAccess.SetEnabled(!value)
		tailscaleServeAccess.SetEnabled(!value)
		funnelAccess.SetEnabled(!value)
		bridgeAccess.SetEnabled(!value)
		saveButton.SetVisible(!value)
		if value {
			cancelButton.SetText("关闭")
		} else {
			cancelButton.SetText("取消")
		}
	}
	setReadOnly(readOnly)
	setAccessActionsVisible(actions.accessActionsVisible)

	result := initial
	saveButton.Clicked().Attach(func() {
		serviceType := config.ServiceBridgeClient
		portText := bridgePort.Text()
		if typeCombo.CurrentIndex() == 1 {
			serviceType = config.ServiceStatic
			portText = staticPort.Text()
		} else if typeCombo.CurrentIndex() == 2 {
			serviceType = config.ServiceProxy
			portText = proxyPort.Text()
		}
		port, parseErr := strconv.Atoi(strings.TrimSpace(portText))
		if parseErr != nil {
			walk.MsgBox(dialog, "配置错误", "本地端口必须是数字。", walk.MsgBoxIconError)
			return
		}
		accessMode := config.AccessRemoteBridge
		remoteAddress, pairToken, lanes := "", "", 0
		if serviceType == config.ServiceBridgeClient && strings.TrimSpace(pairingCode.Text()) != "" {
			parsedRemote, parsedToken, parsedLanes, pairingErr := bridge.ParsePairingCode(pairingCode.Text())
			if pairingErr != nil {
				walk.MsgBox(dialog, "配置错误", pairingErr.Error(), walk.MsgBoxIconError)
				return
			}
			remoteAddress, pairToken, lanes = parsedRemote, parsedToken, parsedLanes
			serviceType = config.ServiceBridgeClient
		} else if initial.Type == config.ServiceBridgeClient && serviceType == config.ServiceBridgeClient {
			serviceType = config.ServiceBridgeClient
			remoteAddress, pairToken, lanes = initial.BridgeRemoteAddr, initial.BridgePairToken, initial.BridgeLaneCount
		}
		if serviceType == config.ServiceBridgeClient && remoteAddress == "" {
			walk.MsgBox(dialog, "配置错误", "Remote Bridge Client 必须粘贴配对码。", walk.MsgBoxIconError)
			return
		}
		if serviceType != config.ServiceBridgeClient {
			switch {
			case ipv6Access.Checked():
				accessMode = config.AccessIPv6Direct
			case tailscaleAccess.Checked():
				accessMode = config.AccessTailscaleDirect
			case tailscaleServeAccess.Checked():
				accessMode = config.AccessTailscaleServe
			case funnelAccess.Checked():
				accessMode = config.AccessFunnel
			case bridgeAccess.Checked():
				accessMode = config.AccessRemoteBridge
			}
		}
		directory := ""
		localAddress := ""
		autoTerminate := false
		if serviceType == config.ServiceStatic {
			directory, autoTerminate = directoryEdit.Text(), staticTerminate.Checked()
		} else if serviceType == config.ServiceProxy {
			localAddress, autoTerminate = addressEdit.Text(), proxyTerminate.Checked()
		}
		result = config.NormalizeService(config.Service{
			ID: initial.ID, Name: nameEdit.Text(), Type: serviceType,
			Directory: directory, LocalAddress: localAddress, Port: port, AccessMode: accessMode,
			AutoStart: autoStart.Checked(), AutoTerminatePort: autoTerminate,
			BridgeRemoteAddr: remoteAddress, BridgePairToken: pairToken, BridgeLaneCount: lanes,
		})
		if result.Type == config.ServiceProxy && result.Port == 0 {
			if parsed, parseURLerr := url.Parse(result.LocalAddress); parseURLerr == nil {
				if parsedPort, parsePortErr := strconv.Atoi(parsed.Port()); parsePortErr == nil {
					result.Port = parsedPort
				}
			}
		}
		if validateErr := config.ValidateService(result); validateErr != nil {
			walk.MsgBox(dialog, "配置错误", validateErr.Error(), walk.MsgBoxIconError)
			return
		}
		dialog.Accept()
	})

	dialog.SetMinMaxSize(walk.Size{Width: 500, Height: 420}, walk.Size{})
	dialog.SetSize(walk.Size{Width: 500, Height: 500})
	if dialog.Run() != int(walk.DlgCmdOK) {
		return config.Service{}, false
	}
	return result, true
}

func newDialogSection(parent walk.Container) *walk.Composite {
	section, _ := walk.NewComposite(parent)
	layout := walk.NewVBoxLayout()
	layout.SetMargins(walk.Margins{})
	layout.SetSpacing(8)
	_ = layout.SetAlignment(walk.AlignHNearVNear)
	section.SetLayout(layout)
	return section
}

func addDialogRow(parent walk.Container, labelText string) *walk.Composite {
	row, _ := newDialogRow(parent, labelText)
	return row
}

func newDialogRow(parent walk.Container, labelText string) (*walk.Composite, *walk.Label) {
	row, _ := walk.NewComposite(parent)
	layout := walk.NewHBoxLayout()
	layout.SetMargins(walk.Margins{})
	layout.SetSpacing(8)
	row.SetLayout(layout)
	label, _ := walk.NewLabel(row)
	label.SetText(labelText)
	label.SetMinMaxSize(walk.Size{Width: 110, Height: 0}, walk.Size{Width: 110, Height: 0})
	return row, label
}

func addDialogLine(parent walk.Container, labelText, value string) *walk.LineEdit {
	_, edit := addDialogLabeledLine(parent, labelText, value)
	return edit
}

func addDialogLabeledLine(parent walk.Container, labelText, value string) (*walk.Label, *walk.LineEdit) {
	row, label := newDialogRow(parent, labelText)
	edit, _ := walk.NewLineEdit(row)
	edit.SetText(value)
	row.Layout().(*walk.BoxLayout).SetStretchFactor(edit, 1)
	return label, edit
}

func addDialogCombo(parent walk.Container, labelText string, values []string) *walk.ComboBox {
	row := addDialogRow(parent, labelText)
	combo, _ := walk.NewDropDownBox(row)
	_ = combo.SetModel(values)
	row.Layout().(*walk.BoxLayout).SetStretchFactor(combo, 1)
	return combo
}

func addDialogCheck(parent walk.Container, text string, checked bool) *walk.CheckBox {
	check, _ := walk.NewCheckBox(parent)
	check.SetText(text)
	check.SetChecked(checked)
	return check
}

func addServiceDialogAction(parent walk.Container, text string, handler func()) *walk.PushButton {
	button, _ := walk.NewPushButton(parent)
	button.SetText(text)
	button.Clicked().Attach(handler)
	return button
}

func servicePort(service config.Service, fallback int) string {
	if service.Port > 0 {
		return strconv.Itoa(service.Port)
	}
	return strconv.Itoa(fallback)
}

func editSettings(owner walk.Form, tailscalePath string) (string, bool) {
	dialog, err := walk.NewDialog(owner)
	if err != nil {
		return "", false
	}
	defer dialog.Dispose()
	dialog.SetTitle("PortPilot 设置")
	dialog.SetSize(walk.Size{Width: 560, Height: 150})
	layout := walk.NewVBoxLayout()
	layout.SetMargins(walk.Margins{HNear: 14, VNear: 14, HFar: 14, VFar: 14})
	layout.SetSpacing(10)
	dialog.SetLayout(layout)
	row, _ := walk.NewComposite(dialog)
	rowLayout := walk.NewHBoxLayout()
	rowLayout.SetMargins(walk.Margins{})
	rowLayout.SetSpacing(8)
	row.SetLayout(rowLayout)
	label, _ := walk.NewLabel(row)
	label.SetText("Tailscale CLI")
	edit, _ := walk.NewLineEdit(row)
	edit.SetText(tailscalePath)
	buttons, _ := walk.NewComposite(dialog)
	buttonLayout := walk.NewHBoxLayout()
	buttonLayout.SetMargins(walk.Margins{})
	buttonLayout.SetSpacing(8)
	buttons.SetLayout(buttonLayout)
	_, _ = walk.NewHSpacer(buttons)
	save, _ := walk.NewPushButton(buttons)
	save.SetText("保存")
	cancel, _ := walk.NewPushButton(buttons)
	cancel.SetText("取消")
	cancel.Clicked().Attach(dialog.Cancel)
	save.Clicked().Attach(func() {
		if strings.TrimSpace(edit.Text()) == "" {
			walk.MsgBox(dialog, "配置错误", "Tailscale CLI 路径不能为空。", walk.MsgBoxIconError)
			return
		}
		dialog.Accept()
	})
	if dialog.Run() != int(walk.DlgCmdOK) {
		return "", false
	}
	return strings.TrimSpace(edit.Text()), true
}
