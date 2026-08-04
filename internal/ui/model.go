package ui

import (
	"github.com/BeyondXinXin/portpilot/internal/config"
	"github.com/BeyondXinXin/portpilot/internal/manager"
	"github.com/lxn/walk"
)

type serviceTableModel struct {
	walk.TableModelBase
	items []manager.Snapshot
}

func (m *serviceTableModel) RowCount() int {
	return len(m.items)
}

func (m *serviceTableModel) Value(row, column int) any {
	if row < 0 || row >= len(m.items) {
		return ""
	}
	item := m.items[row]
	switch column {
	case 0:
		return item.Service.Name
	case 1:
		if item.Service.Type == config.ServiceStatic {
			return "静态文件"
		}
		return "本地代理"
	case 2:
		return manager.StatusLabel(item.Status)
	case 3:
		return item.Service.LocalAddress
	case 4:
		return item.PublicURL
	case 5:
		return manager.AccessModeLabel(item.AccessMode)
	default:
		return ""
	}
}

func (m *serviceTableModel) setItems(items []manager.Snapshot) {
	m.items = items
	m.PublishRowsReset()
}

func (m *serviceTableModel) item(index int) (manager.Snapshot, bool) {
	if index < 0 || index >= len(m.items) {
		return manager.Snapshot{}, false
	}
	return m.items[index], true
}
