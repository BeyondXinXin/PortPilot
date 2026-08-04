package tunnel

import (
	"os"
	"testing"
)

func TestExtractPublicURL(t *testing.T) {
	got := extractPublicURL("Available on the internet:\nhttps://desktop.tail123.ts.net:8443/")
	if got != "https://desktop.tail123.ts.net:8443/" {
		t.Fatalf("got %q", got)
	}
}

func TestNoFunnelConfig(t *testing.T) {
	if !noFunnelConfig("{}") {
		t.Fatal("empty JSON status should be clean")
	}
	if noFunnelConfig(`{"TCP":{"443":{}}}`) {
		t.Fatal("non-empty status should not be clean")
	}
}

func TestInstalledTailscaleResetAndVerify(t *testing.T) {
	if os.Getenv("PORTPILOT_TAILSCALE_INTEGRATION") != "1" {
		t.Skip("set PORTPILOT_TAILSCALE_INTEGRATION=1 to clear and verify installed Funnel state")
	}
	path := os.Getenv("PORTPILOT_TAILSCALE_PATH")
	if path == "" {
		path = "tailscale.exe"
	}
	if err := New(path).Reset(); err != nil {
		t.Fatal(err)
	}
}
