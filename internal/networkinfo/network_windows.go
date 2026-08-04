package networkinfo

import (
	"errors"
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"unsafe"

	"github.com/BeyondXinXin/portpilot/internal/winutil"
	"golang.org/x/sys/windows"
)

const (
	ipAdapterAddressTransient = 0x02
	ipSuffixOriginManual      = 1
	ipSuffixOriginWellKnown   = 2
	ipSuffixOriginLinkLayer   = 4
	ipSuffixOriginRandom      = 5
	ipDadStatePreferred       = 4
)

type Address struct {
	IP            net.IP
	InterfaceName string
	Temporary     bool
	Rank          int
}

type Info struct {
	IPv6      []Address
	Tailscale []Address
}

type FirewallStatus string

const (
	FirewallAllowed FirewallStatus = "allowed"
	FirewallBlocked FirewallStatus = "blocked"
	FirewallUnknown FirewallStatus = "unknown"
)

func Detect() (Info, error) {
	bufferSize := uint32(15 * 1024)
	for attempt := 0; attempt < 3; attempt++ {
		buffer := make([]byte, bufferSize)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buffer[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_INCLUDE_PREFIX, 0, first, &bufferSize)
		if errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
			continue
		}
		if err != nil {
			return Info{}, fmt.Errorf("GetAdaptersAddresses failed: %w", err)
		}
		return collect(first), nil
	}
	return Info{}, errors.New("GetAdaptersAddresses buffer size kept changing")
}

func collect(first *windows.IpAdapterAddresses) Info {
	var info Info
	for adapter := first; adapter != nil; adapter = adapter.Next {
		if adapter.OperStatus != windows.IfOperStatusUp || adapter.IfType == windows.IF_TYPE_SOFTWARE_LOOPBACK {
			continue
		}
		name := windows.UTF16PtrToString(adapter.FriendlyName)
		description := windows.UTF16PtrToString(adapter.Description)
		isTailscaleAdapter := strings.Contains(strings.ToLower(name+" "+description), "tailscale") || adapter.IfType == windows.IF_TYPE_TUNNEL
		for unicast := adapter.FirstUnicastAddress; unicast != nil; unicast = unicast.Next {
			if unicast.DadState != ipDadStatePreferred {
				continue
			}
			ip := append(net.IP(nil), unicast.Address.IP()...)
			if ip == nil {
				continue
			}
			if ipv4 := ip.To4(); ipv4 != nil {
				if isTailscaleAdapter && isTailscaleIPv4(ipv4) {
					info.Tailscale = append(info.Tailscale, Address{IP: append(net.IP(nil), ipv4...), InterfaceName: name})
				}
				continue
			}
			if !isPublicIPv6(ip) || isTailscaleAdapter {
				continue
			}
			temporary := unicast.Flags&ipAdapterAddressTransient != 0 || unicast.SuffixOrigin == ipSuffixOriginRandom
			info.IPv6 = append(info.IPv6, Address{
				IP: ip, InterfaceName: name, Temporary: temporary,
				Rank: ipv6Rank(temporary, unicast.SuffixOrigin),
			})
		}
	}
	sort.SliceStable(info.IPv6, func(i, j int) bool {
		if info.IPv6[i].Rank != info.IPv6[j].Rank {
			return info.IPv6[i].Rank < info.IPv6[j].Rank
		}
		return info.IPv6[i].IP.String() < info.IPv6[j].IP.String()
	})
	sort.SliceStable(info.Tailscale, func(i, j int) bool {
		return info.Tailscale[i].IP.String() < info.Tailscale[j].IP.String()
	})
	return info
}

func (info Info) PrimaryIPv6() (Address, bool) {
	if len(info.IPv6) == 0 {
		return Address{}, false
	}
	return info.IPv6[0], true
}

func (info Info) PrimaryTailscale() (Address, bool) {
	if len(info.Tailscale) == 0 {
		return Address{}, false
	}
	return info.Tailscale[0], true
}

func CheckInboundTCP(port int) (FirewallStatus, error) {
	portText := strconv.Itoa(port)
	script := strings.Join([]string{
		"$ErrorActionPreference='Stop'",
		"$port=" + portText,
		"$filters=Get-NetFirewallRule -PolicyStore ActiveStore -Enabled True -Direction Inbound -Action Allow | Get-NetFirewallPortFilter",
		"$match=$filters | Where-Object {",
		"  if ($_.Protocol -ne 'TCP') { return $false }",
		"  $value=[string]$_.LocalPort",
		"  if ($value -eq 'Any' -or $value -eq [string]$port) { return $true }",
		"  foreach ($part in ($value -split '[,\\s]+')) {",
		"    $single=0",
		"    if ([int]::TryParse($part, [ref]$single) -and $single -eq $port) { return $true }",
		"    $bounds=$part -split '-'",
		"    $low=0; $high=0",
		"    if ($bounds.Count -eq 2 -and [int]::TryParse($bounds[0], [ref]$low) -and [int]::TryParse($bounds[1], [ref]$high) -and $port -ge $low -and $port -le $high) { return $true }",
		"  }",
		"  return $false",
		"}",
		"if ($match) { exit 0 } else { exit 3 }",
	}, "\r\n")
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	winutil.HideWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return FirewallAllowed, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 3 {
		return FirewallBlocked, nil
	}
	return FirewallUnknown, fmt.Errorf("firewall query failed: %w: %s", err, strings.TrimSpace(string(output)))
}

func isPublicIPv6(ip net.IP) bool {
	if ip.To4() != nil || !ip.IsGlobalUnicast() || ip.IsLinkLocalUnicast() {
		return false
	}
	return len(ip) == net.IPv6len && ip[0]&0xfe != 0xfc
}

func isTailscaleIPv4(ip net.IP) bool {
	ip = ip.To4()
	return ip != nil && ip[0] == 100 && ip[1] >= 64 && ip[1] <= 127
}

func ipv6Rank(temporary bool, suffixOrigin int32) int {
	if temporary {
		return 2
	}
	switch suffixOrigin {
	case ipSuffixOriginManual, ipSuffixOriginWellKnown, ipSuffixOriginLinkLayer:
		return 0
	default:
		return 1
	}
}
