package manager

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BeyondXinXin/portpilot/internal/config"
	"github.com/BeyondXinXin/portpilot/internal/runlog"
	"github.com/BeyondXinXin/portpilot/internal/tunnel"
)

type fakeTunnel struct {
	started int
	stopped int
	reset   int
}

func (f *fakeTunnel) Reset() error {
	f.reset++
	return nil
}

func (f *fakeTunnel) Start(serviceID, target string) (tunnel.Assignment, error) {
	f.started++
	return tunnel.Assignment{ServiceID: serviceID, HTTPSPort: 443, PublicURL: "https://test.tail.ts.net"}, nil
}

func (f *fakeTunnel) Stop(serviceID string) error {
	f.stopped++
	return nil
}

func (f *fakeTunnel) Healthy(serviceID string) error {
	return nil
}

func TestStaticServiceLifecycle(t *testing.T) {
	base := t.TempDir()
	content := filepath.Join(base, "site")
	if err := os.Mkdir(content, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(content, "hello.txt"), []byte("portpilot"), 0644); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	service := config.NormalizeService(config.Service{ID: "site", Name: "Website", Type: config.ServiceStatic, Directory: content, Port: port})
	logger, err := runlog.Open(base)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	funnel := &fakeTunnel{}
	manager := New([]config.Service{service}, funnel, logger)

	if err := manager.Start(service.ID); err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/hello.txt", port))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(body) != "portpilot" {
		t.Fatalf("unexpected body %q", body)
	}
	snapshot, _ := manager.Snapshot(service.ID)
	if snapshot.Status != StatusRunning || snapshot.PublicURL == "" {
		t.Fatalf("unexpected runtime state: %+v", snapshot)
	}

	if err := manager.Stop(service.ID); err != nil {
		t.Fatal(err)
	}
	if funnel.started != 1 || funnel.stopped != 1 {
		t.Fatalf("unexpected tunnel calls: start=%d stop=%d", funnel.started, funnel.stopped)
	}
	connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 250*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		t.Fatal("static service port remained open after stop")
	}
}

func TestPortConflictDoesNotStartTunnel(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	base := t.TempDir()
	logger, err := runlog.Open(base)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	funnel := &fakeTunnel{}
	service := config.NormalizeService(config.Service{ID: "conflict", Name: "Conflict", Type: config.ServiceStatic, Directory: base, Port: port})
	manager := New([]config.Service{service}, funnel, logger)

	err = manager.Start(service.ID)
	if err == nil {
		t.Fatal("expected port conflict")
	}
	if funnel.started != 0 {
		t.Fatal("tunnel started despite port conflict")
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}
