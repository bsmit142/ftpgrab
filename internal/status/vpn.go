package status

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// vpnInterval is how often the VPN is checked.
const vpnInterval = 15 * time.Second

// probeName is looked up through the probe server. Any answer, even "no such
// host", proves the tunnel; it must not be in /etc/hosts, which Go checks first.
const probeName = "example.com."

// VPN describes the tunnel the server's traffic must use
type VPN struct {
	Interface string // e.g. wg0
	Probe     string // host:port of a DNS server only reachable through it; optional
	Host      string // the FTP/SFTP server
	Port      int
}

// VPNState is the result of a check
type VPNState struct {
	Interface string    `json:"interface"`
	Connected bool      `json:"connected"`
	Problem   string    `json:"problem,omitempty"` // why not connected
	Up        bool      `json:"up"`
	Addresses []string  `json:"addresses,omitempty"`
	Host      string    `json:"host,omitempty"`
	HostIP    string    `json:"hostIp,omitempty"`
	Routed    bool      `json:"routed"` // the host's traffic leaves through Interface
	Probe     string    `json:"probe,omitempty"`
	ProbeOK   bool      `json:"probeOk"`
	ProbeMs   float64   `json:"probeMs,omitempty"`
	RxBytes   uint64    `json:"rxBytes"`
	TxBytes   uint64    `json:"txBytes"`
	Checked   time.Time `json:"checked"`
}

// SetVPN records the latest VPN check
func (t *Tracker) SetVPN(state VPNState) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.vpn = &state
}

// WatchVPN checks the VPN now and every vpnInterval, logging when it drops
// and recovers. It runs until the process exits.
func (t *Tracker) WatchVPN(v VPN) {
	log.Info().Str("interface", v.Interface).Msg("Watching the VPN")
	var last *VPNState
	for {
		state := v.Check(context.Background())
		t.SetVPN(state)
		switch {
		case !state.Connected && (last == nil || last.Connected || last.Problem != state.Problem):
			log.Warn().Str("interface", v.Interface).Str("problem", state.Problem).Msg("VPN down")
		case state.Connected && last != nil && !last.Connected:
			log.Info().Str("interface", v.Interface).Msg("VPN connected")
		}
		last = &state
		time.Sleep(vpnInterval)
	}
}

// Check runs the checks once. Nothing here needs privileges.
func (v VPN) Check(ctx context.Context) VPNState {
	s := VPNState{Interface: v.Interface, Host: v.Host, Probe: v.Probe, Checked: time.Now()}
	var problems []string

	// 1. The interface exists and is up.
	iface, err := net.InterfaceByName(v.Interface)
	if err != nil {
		s.Problem = fmt.Sprintf("%s does not exist", v.Interface)
		return s
	}
	s.Up = iface.Flags&net.FlagUp != 0
	if !s.Up {
		problems = append(problems, v.Interface+" is down")
	}
	local := map[netip.Addr]bool{}
	if addrs, err := iface.Addrs(); err == nil {
		for _, a := range addrs {
			if p, err := netip.ParsePrefix(a.String()); err == nil {
				local[p.Addr().Unmap()] = true
				s.Addresses = append(s.Addresses, p.Addr().String())
			}
		}
	}
	s.RxBytes = readCounter(v.Interface, "rx_bytes")
	s.TxBytes = readCounter(v.Interface, "tx_bytes")

	// 2. The kernel routes the server through it: a UDP "connection" sends
	// nothing but picks the source address the route would use.
	if v.Host != "" {
		if routed, ip, err := routedVia(ctx, v.Host, v.Port, local); err != nil {
			problems = append(problems, err.Error())
		} else {
			s.HostIP, s.Routed = ip, routed
			if !routed {
				problems = append(problems, fmt.Sprintf("%s is not routed through %s", v.Host, v.Interface))
			}
		}
	}

	// 3. A server only reachable through the tunnel answers.
	if v.Probe != "" {
		start := time.Now()
		if err := probeDNS(ctx, v.Probe); err != nil {
			problems = append(problems, fmt.Sprintf("no answer from %s through the tunnel", v.Probe))
		} else {
			s.ProbeOK = true
			s.ProbeMs = float64(time.Since(start).Microseconds()) / 1000
		}
	}

	s.Problem = strings.Join(problems, "; ")
	s.Connected = len(problems) == 0
	return s
}

func routedVia(ctx context.Context, host string, port int, local map[netip.Addr]bool) (bool, string, error) {
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return false, "", fmt.Errorf("cannot resolve %s", host)
	}
	ip := ips[0]
	for _, candidate := range ips { // prefer IPv4, as the connection would
		if candidate.Unmap().Is4() {
			ip = candidate
			break
		}
	}
	if port == 0 {
		port = 22
	}
	conn, err := net.Dial("udp", net.JoinHostPort(ip.Unmap().String(), strconv.Itoa(port)))
	if err != nil {
		return false, ip.String(), fmt.Errorf("no route to %s", host)
	}
	defer conn.Close()
	src, err := netip.ParseAddrPort(conn.LocalAddr().String())
	if err != nil {
		return false, ip.String(), err
	}
	return local[src.Addr().Unmap()], ip.Unmap().String(), nil
}

func probeDNS(ctx context.Context, server string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "udp", server)
		},
	}
	_, err := r.LookupNetIP(ctx, "ip4", probeName)
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return nil // answered
	}
	return err
}

func readCounter(iface, name string) uint64 {
	b, err := os.ReadFile("/sys/class/net/" + iface + "/statistics/" + name)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	return n
}
