package portcheck

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/BeyondXinXin/portpilot/internal/winutil"
)

type Info struct {
	Port        int
	Occupied    bool
	PID         int
	ProcessName string
}

func Inspect(port int) (Info, error) {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4zero, Port: port})
	if err == nil {
		_ = listener.Close()
		return Info{Port: port}, nil
	}
	pid, lookupErr := listeningPID(port)
	if lookupErr != nil {
		return Info{Port: port, Occupied: true}, nil
	}
	return Info{Port: port, Occupied: true, PID: pid, ProcessName: processName(pid)}, nil
}

func WaitUntilFree(port int, timeout time.Duration) (Info, error) {
	deadline := time.Now().Add(timeout)
	for {
		info, err := Inspect(port)
		if err != nil {
			return info, err
		}
		if !info.Occupied {
			return info, nil
		}
		if time.Now().After(deadline) {
			return info, fmt.Errorf("端口 %d 仍被占用", port)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func Terminate(info Info) error {
	if !info.Occupied || info.PID == 0 {
		return errors.New("无法确定端口占用进程")
	}
	if info.PID == os.Getpid() || info.PID == 4 {
		return fmt.Errorf("拒绝终止受保护进程 PID %d", info.PID)
	}
	cmd := exec.Command("taskkill.exe", "/PID", strconv.Itoa(info.PID), "/T", "/F")
	winutil.HideWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("关闭进程失败: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func listeningPID(port int) (int, error) {
	cmd := exec.Command("netstat.exe", "-ano", "-p", "tcp")
	winutil.HideWindow(cmd)
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	return parseNetstatPID(string(output), port)
}

func parseNetstatPID(output string, port int) (int, error) {
	suffix := ":" + strconv.Itoa(port)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || !strings.EqualFold(fields[0], "TCP") {
			continue
		}
		localAddress := fields[1]
		state := fields[len(fields)-2]
		if !strings.HasSuffix(localAddress, suffix) || !strings.EqualFold(state, "LISTENING") {
			continue
		}
		pid, err := strconv.Atoi(fields[len(fields)-1])
		if err == nil {
			return pid, nil
		}
	}
	return 0, errors.New("未找到监听进程")
}

func processName(pid int) string {
	if pid == 0 {
		return ""
	}
	cmd := exec.Command("tasklist.exe", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH")
	winutil.HideWindow(cmd)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	records, err := csv.NewReader(strings.NewReader(string(output))).ReadAll()
	if err != nil || len(records) == 0 || len(records[0]) == 0 {
		return ""
	}
	return strings.TrimSpace(records[0][0])
}
