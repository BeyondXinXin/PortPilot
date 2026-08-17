package ui

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/BeyondXinXin/portpilot/internal/config"
	"github.com/lxn/walk"
)

func editService(owner walk.Form, initial config.Service) (config.Service, bool) {
	dialog, err := walk.NewDialog(owner)
	if err != nil {
		return config.Service{}, false
	}
	defer dialog.Dispose()
	dialog.SetTitle("服务配置")
	layout := walk.NewVBoxLayout()
	layout.SetMargins(walk.Margins{HNear: 14, VNear: 14, HFar: 14, VFar: 14})
	layout.SetSpacing(10)
	dialog.SetLayout(layout)

	form, _ := walk.NewComposite(dialog)
	grid := walk.NewGridLayout()
	grid.SetMargins(walk.Margins{})
	grid.SetSpacing(8)
	grid.SetColumnStretchFactor(1, 1)
	form.SetLayout(grid)

	nameEdit := addLineRow(form, 0, "名称", initial.Name)
	typeCombo := addComboRow(form, 1, "类型", []string{"静态文件服务", "本地代理服务"})
	if initial.Type == config.ServiceProxy {
		typeCombo.SetCurrentIndex(1)
	} else {
		typeCombo.SetCurrentIndex(0)
	}

	directoryLabel, _ := walk.NewLabel(form)
	directoryLabel.SetText("目录")
	grid.SetRange(directoryLabel, walk.Rectangle{X: 0, Y: 2, Width: 1, Height: 1})
	directoryRow, _ := walk.NewComposite(form)
	directoryLayout := walk.NewHBoxLayout()
	directoryLayout.SetMargins(walk.Margins{})
	directoryLayout.SetSpacing(6)
	directoryRow.SetLayout(directoryLayout)
	grid.SetRange(directoryRow, walk.Rectangle{X: 1, Y: 2, Width: 1, Height: 1})
	directoryEdit, _ := walk.NewLineEdit(directoryRow)
	directoryEdit.SetText(initial.Directory)
	browseButton, _ := walk.NewPushButton(directoryRow)
	browseButton.SetText("浏览...")
	browseButton.Clicked().Attach(func() {
		fileDialog := new(walk.FileDialog)
		fileDialog.Title = "选择静态文件目录"
		if ok, browseErr := fileDialog.ShowBrowseFolder(dialog); browseErr == nil && ok {
			directoryEdit.SetText(fileDialog.FilePath)
		}
	})

	addressEdit := addLineRow(form, 3, "本地地址", initial.LocalAddress)
	portEdit := addLineRow(form, 4, "本地端口", strconv.Itoa(initial.Port))
	autoStart := addCheckRow(form, 5, "自动启动", initial.AutoStart)
	autoTerminate := addCheckRow(form, 6, "自动关闭端口占用进程", initial.AutoTerminatePort)

	accessGroup, _ := walk.NewGroupBox(form)
	accessGroup.SetTitle("Access Mode")
	accessLayout := walk.NewVBoxLayout()
	accessLayout.SetMargins(walk.Margins{HNear: 8, VNear: 6, HFar: 8, VFar: 6})
	accessLayout.SetSpacing(4)
	accessGroup.SetLayout(accessLayout)
	grid.SetRange(accessGroup, walk.Rectangle{X: 0, Y: 7, Width: 2, Height: 1})
	autoAccess, _ := walk.NewRadioButton(accessGroup)
	autoAccess.SetText("Auto（推荐）")
	ipv6Access, _ := walk.NewRadioButton(accessGroup)
	ipv6Access.SetText("IPv6 Direct - 最快，公网直连")
	tailscaleAccess, _ := walk.NewRadioButton(accessGroup)
	tailscaleAccess.SetText("Tailscale Direct - Tailnet 私网")
	tailscaleServeAccess, _ := walk.NewRadioButton(accessGroup)
	tailscaleServeAccess.SetText("Tailscale Serve - Tailnet HTTPS（Harness 推荐）")
	funnelAccess, _ := walk.NewRadioButton(accessGroup)
	funnelAccess.SetText("Tailscale Funnel - 公网中继")
	switch initial.AccessMode {
	case config.AccessIPv6Direct:
		ipv6Access.SetChecked(true)
	case config.AccessTailscaleDirect:
		tailscaleAccess.SetChecked(true)
	case config.AccessTailscaleServe:
		tailscaleServeAccess.SetChecked(true)
	case config.AccessFunnel:
		funnelAccess.SetChecked(true)
	default:
		autoAccess.SetChecked(true)
	}

	updateType := func() {
		isStatic := typeCombo.CurrentIndex() == 0
		directoryEdit.SetEnabled(isStatic)
		browseButton.SetEnabled(isStatic)
		addressEdit.SetReadOnly(isStatic)
		if isStatic {
			port, _ := strconv.Atoi(strings.TrimSpace(portEdit.Text()))
			if port > 0 {
				addressEdit.SetText(fmt.Sprintf("http://127.0.0.1:%d", port))
			}
		}
	}
	typeCombo.CurrentIndexChanged().Attach(updateType)
	portEdit.TextChanged().Attach(updateType)
	updateType()

	buttons, _ := walk.NewComposite(dialog)
	buttonLayout := walk.NewHBoxLayout()
	buttonLayout.SetMargins(walk.Margins{})
	buttonLayout.SetSpacing(8)
	buttons.SetLayout(buttonLayout)
	spacer, _ := walk.NewHSpacer(buttons)
	_ = spacer
	saveButton, _ := walk.NewPushButton(buttons)
	saveButton.SetText("保存")
	cancelButton, _ := walk.NewPushButton(buttons)
	cancelButton.SetText("取消")
	cancelButton.Clicked().Attach(dialog.Cancel)

	result := initial
	saveButton.Clicked().Attach(func() {
		port, parseErr := strconv.Atoi(strings.TrimSpace(portEdit.Text()))
		if parseErr != nil {
			walk.MsgBox(dialog, "配置错误", "本地端口必须是数字。", walk.MsgBoxIconError)
			return
		}
		serviceType := config.ServiceStatic
		if typeCombo.CurrentIndex() == 1 {
			serviceType = config.ServiceProxy
		}
		accessMode := config.AccessAuto
		switch {
		case ipv6Access.Checked():
			accessMode = config.AccessIPv6Direct
		case tailscaleAccess.Checked():
			accessMode = config.AccessTailscaleDirect
		case tailscaleServeAccess.Checked():
			accessMode = config.AccessTailscaleServe
		case funnelAccess.Checked():
			accessMode = config.AccessFunnel
		}
		result = config.NormalizeService(config.Service{
			ID: initial.ID, Name: nameEdit.Text(), Type: serviceType,
			Directory: directoryEdit.Text(), LocalAddress: addressEdit.Text(), Port: port, AccessMode: accessMode,
			AutoStart: autoStart.Checked(), AutoTerminatePort: autoTerminate.Checked(),
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

	dialog.SetMinMaxSize(walk.Size{Width: 400, Height: 560}, walk.Size{})
	dialog.SetSize(walk.Size{Width: 400, Height: 560})
	if dialog.Run() != int(walk.DlgCmdOK) {
		return config.Service{}, false
	}
	return result, true
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

func addLineRow(parent *walk.Composite, row int, labelText, value string) *walk.LineEdit {
	grid := parent.Layout().(*walk.GridLayout)
	label, _ := walk.NewLabel(parent)
	label.SetText(labelText)
	grid.SetRange(label, walk.Rectangle{X: 0, Y: row, Width: 1, Height: 1})
	edit, _ := walk.NewLineEdit(parent)
	edit.SetText(value)
	grid.SetRange(edit, walk.Rectangle{X: 1, Y: row, Width: 1, Height: 1})
	return edit
}

func addComboRow(parent *walk.Composite, row int, labelText string, values []string) *walk.ComboBox {
	grid := parent.Layout().(*walk.GridLayout)
	label, _ := walk.NewLabel(parent)
	label.SetText(labelText)
	grid.SetRange(label, walk.Rectangle{X: 0, Y: row, Width: 1, Height: 1})
	combo, _ := walk.NewComboBox(parent)
	_ = combo.SetModel(values)
	grid.SetRange(combo, walk.Rectangle{X: 1, Y: row, Width: 1, Height: 1})
	return combo
}

func addCheckRow(parent *walk.Composite, row int, text string, checked bool) *walk.CheckBox {
	grid := parent.Layout().(*walk.GridLayout)
	check, _ := walk.NewCheckBox(parent)
	check.SetText(text)
	check.SetChecked(checked)
	grid.SetRange(check, walk.Rectangle{X: 1, Y: row, Width: 1, Height: 1})
	return check
}
