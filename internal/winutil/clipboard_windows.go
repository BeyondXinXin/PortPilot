package winutil

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// CopyText uses the Windows inbox clip.exe process instead of a GUI-framework
// clipboard window. This is reliable in applications built with windowsgui.
func CopyText(text string) error {
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	command := exec.Command(filepath.Join(systemRoot, "System32", "clip.exe"))
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	input, err := command.StdinPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return err
	}
	_, writeErr := io.WriteString(input, text)
	closeErr := input.Close()
	waitErr := command.Wait()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return waitErr
}
