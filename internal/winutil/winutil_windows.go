package winutil

import (
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	errorAlreadyExists syscall.Errno = 183
	swRestore                        = 9
)

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	user32                  = syscall.NewLazyDLL("user32.dll")
	procCreateMutexW        = kernel32.NewProc("CreateMutexW")
	procCloseHandle         = kernel32.NewProc("CloseHandle")
	procFindWindowW         = user32.NewProc("FindWindowW")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
)

type SingleInstance struct {
	handle uintptr
	once   sync.Once
}

func AcquireSingleInstance(name string) (*SingleInstance, bool, error) {
	namePointer, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, false, err
	}
	handle, _, callErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(namePointer)))
	if handle == 0 {
		if callErr != syscall.Errno(0) {
			return nil, false, callErr
		}
		return nil, false, fmt.Errorf("CreateMutexW failed")
	}
	if callErr == errorAlreadyExists {
		_, _, _ = procCloseHandle.Call(handle)
		return nil, true, nil
	}
	return &SingleInstance{handle: handle}, false, nil
}

func (instance *SingleInstance) Close() error {
	if instance == nil || instance.handle == 0 {
		return nil
	}
	var closeErr error
	instance.once.Do(func() {
		result, _, callErr := procCloseHandle.Call(instance.handle)
		if result == 0 && callErr != syscall.Errno(0) {
			closeErr = callErr
		}
		instance.handle = 0
	})
	return closeErr
}

func ActivateWindow(title string, timeout time.Duration) bool {
	titlePointer, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return false
	}
	deadline := time.Now().Add(timeout)
	for {
		handle, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(titlePointer)))
		if handle != 0 {
			_, _, _ = procShowWindow.Call(handle, swRestore)
			_, _, _ = procSetForegroundWindow.Call(handle)
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

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
