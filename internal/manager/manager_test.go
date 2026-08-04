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
	"github.com/BeyondXinXin/portpilot/internal/networkinfo"
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
	service := config.NormalizeService(config.Service{ID: "site", Name: "Website", Type: config.ServiceStatic, Directory: content, Port: port, AccessMode: config.AccessFunnel})
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
	service := config.NormalizeService(config.Service{ID: "conflict", Name: "Conflict", Type: config.ServiceStatic, Directory: base, Port: port, AccessMode: config.AccessFunnel})
	manager := New([]config.Service{service}, funnel, logger)

	err = manager.Start(service.ID)
	if err == nil {
		t.Fatal("expected port conflict")
	}
	if funnel.started != 0 {
		t.Fatal("tunnel started despite port conflict")
	}
}

func TestDirectOnlyShutdownDoesNotRequireTailscaleReset(t *testing.T) {
	base := t.TempDir()
	logger, err := runlog.Open(base)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	funnel := &fakeTunnel{}
	serviceManager := New(nil, funnel, logger)
	if err := serviceManager.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if funnel.reset != 0 {
		t.Fatalf("unused Funnel was reset %d times during shutdown", funnel.reset)
	}
}

func TestFunnelShutdownPerformsFinalReset(t *testing.T) {
	base := t.TempDir()
	logger, err := runlog.Open(base)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	funnel := &fakeTunnel{}
	service := config.NormalizeService(config.Service{
		ID: "funnel", Name: "Funnel", Type: config.ServiceStatic,
		Directory: base, Port: freePort(t), AccessMode: config.AccessFunnel,
	})
	serviceManager := New([]config.Service{service}, funnel, logger)
	if err := serviceManager.Start(service.ID); err != nil {
		t.Fatal(err)
	}
	if err := serviceManager.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if funnel.started != 1 || funnel.stopped != 1 || funnel.reset != 1 {
		t.Fatalf("unexpected Funnel cleanup: start=%d stop=%d reset=%d", funnel.started, funnel.stopped, funnel.reset)
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

func TestAutoAccessPriority(t *testing.T) {
	info := networkinfo.Info{
		IPv6:      []networkinfo.Address{{IP: net.ParseIP("240e:305::1")}},
		Tailscale: []networkinfo.Address{{IP: net.ParseIP("100.100.100.100")}},
	}
	mode, _, ok := selectAccessMode(config.AccessAuto, info)
	if !ok || mode != config.AccessIPv6Direct {
		t.Fatalf("expected IPv6 Direct, got %s", mode)
	}
	info.IPv6 = nil
	mode, _, ok = selectAccessMode(config.AccessAuto, info)
	if !ok || mode != config.AccessTailscaleDirect {
		t.Fatalf("expected Tailscale Direct, got %s", mode)
	}
	info.Tailscale = nil
	mode, _, ok = selectAccessMode(config.AccessAuto, info)
	if !ok || mode != config.AccessFunnel {
		t.Fatalf("expected Funnel fallback, got %s", mode)
	}
}

func TestInstalledDirectModes(t *testing.T) {
	if os.Getenv("PORTPILOT_DIRECT_INTEGRATION") != "1" {
		t.Skip("set PORTPILOT_DIRECT_INTEGRATION=1 to test installed Direct adapters")
	}
	for _, mode := range []config.AccessMode{config.AccessIPv6Direct, config.AccessTailscaleDirect} {
		t.Run(string(mode), func(t *testing.T) {
			base := t.TempDir()
			content := filepath.Join(base, "site")
			if err := os.Mkdir(content, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(content, "mode.txt"), []byte(mode), 0644); err != nil {
				t.Fatal(err)
			}
			logger, err := runlog.Open(base)
			if err != nil {
				t.Fatal(err)
			}
			defer logger.Close()
			port := freePort(t)
			service := config.NormalizeService(config.Service{
				ID: "direct-" + string(mode), Name: string(mode), Type: config.ServiceStatic,
				Directory: content, Port: port, AccessMode: mode,
			})
			funnel := &fakeTunnel{}
			serviceManager := New([]config.Service{service}, funnel, logger)
			if err := serviceManager.Start(service.ID); err != nil {
				t.Fatal(err)
			}
			defer serviceManager.Stop(service.ID)
			snapshot, _ := serviceManager.Snapshot(service.ID)
			client := &http.Client{Timeout: 3 * time.Second}
			response, err := client.Get(snapshot.PublicURL + "/mode.txt")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if string(body) != string(mode) {
				t.Fatalf("unexpected response %q from %s", body, snapshot.PublicURL)
			}
			if funnel.started != 0 {
				t.Fatal("Direct mode unexpectedly started Funnel")
			}
		})
	}
}
