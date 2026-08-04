package winutil

import (
	"fmt"
	"os/exec"
	"syscall"
)

func HideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

func Open(path string) error {
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", path)
	HideWindow(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("打开失败: %w", err)
	}
	return nil
}
