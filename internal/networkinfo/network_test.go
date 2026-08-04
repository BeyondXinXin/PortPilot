package networkinfo

import (
	"net"
	"testing"
)

func TestAddressFilters(t *testing.T) {
	for _, value := range []string{"240e:305:18f0:5600::1", "2606:4700:4700::1111"} {
		if !isPublicIPv6(net.ParseIP(value)) {
			t.Fatalf("expected public IPv6: %s", value)
		}
	}
	for _, value := range []string{"fe80::1", "fd7a:115c:a1e0::1", "fc00::1", "127.0.0.1"} {
		if isPublicIPv6(net.ParseIP(value)) {
			t.Fatalf("unexpected public IPv6: %s", value)
		}
	}
	if !isTailscaleIPv4(net.ParseIP("100.116.152.125")) {
		t.Fatal("expected Tailscale CGNAT address")
	}
	if isTailscaleIPv4(net.ParseIP("100.128.0.1")) {
		t.Fatal("address is outside 100.64.0.0/10")
	}
}

func TestIPv6Rank(t *testing.T) {
	if ipv6Rank(false, ipSuffixOriginLinkLayer) >= ipv6Rank(false, 3) {
		t.Fatal("stable IPv6 should rank before other non-temporary IPv6")
	}
	if ipv6Rank(false, 3) >= ipv6Rank(true, ipSuffixOriginRandom) {
		t.Fatal("non-temporary IPv6 should rank before temporary IPv6")
	}
}

func TestDetectInstalledNetwork(t *testing.T) {
	info, err := Detect()
	if err != nil {
		t.Fatal(err)
	}
	if address, ok := info.PrimaryIPv6(); ok {
		t.Logf("IPv6 Direct: %s (%s, temporary=%v)", address.IP, address.InterfaceName, address.Temporary)
	}
	if address, ok := info.PrimaryTailscale(); ok {
		t.Logf("Tailscale Direct: %s (%s)", address.IP, address.InterfaceName)
	}
}

func TestFirewallCheckReturnsStatus(t *testing.T) {
	status, err := CheckInboundTCP(8081)
	if err != nil && status != FirewallUnknown {
		t.Fatalf("unexpected firewall result: %s, %v", status, err)
	}
	t.Logf("firewall status for TCP 8081: %s (%v)", status, err)
}
