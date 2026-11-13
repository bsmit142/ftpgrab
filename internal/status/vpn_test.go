package status

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loopback finds the loopback interface's name (lo on Linux).
func loopback(t *testing.T) string {
	ifaces, err := net.Interfaces()
	require.NoError(t, err)
	for _, i := range ifaces {
		if i.Flags&net.FlagLoopback != 0 {
			return i.Name
		}
	}
	t.Skip("no loopback interface")
	return ""
}

// fakeDNS answers every query with "no such host".
func fakeDNS(t *testing.T) string {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			q := buf[:n]
			end := 12 // the question: labels, a zero byte, then type and class
			for end < n && q[end] != 0 {
				end += int(q[end]) + 1
			}
			end += 5
			if end > n {
				continue
			}
			resp := append([]byte{q[0], q[1], 0x81, 0x83, 0, 1, 0, 0, 0, 0, 0, 0}, q[12:end]...)
			_, _ = pc.WriteTo(resp, addr)
		}
	}()
	return pc.LocalAddr().String()
}

// closedPort is a UDP address nothing listens on.
func closedPort(t *testing.T) string {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := pc.LocalAddr().String()
	_ = pc.Close()
	return addr
}

func TestCheckMissingInterface(t *testing.T) {
	s := VPN{Interface: "nope0"}.Check(context.Background())
	assert.False(t, s.Connected)
	assert.Equal(t, "nope0 does not exist", s.Problem)
}

func TestCheckConnected(t *testing.T) {
	lo := loopback(t)
	s := VPN{Interface: lo, Host: "127.0.0.1", Port: 22, Probe: fakeDNS(t)}.Check(context.Background())
	assert.True(t, s.Up)
	assert.True(t, s.Routed, "127.0.0.1 leaves through %s", lo)
	assert.Equal(t, "127.0.0.1", s.HostIP)
	assert.True(t, s.ProbeOK)
	assert.True(t, s.Connected, s.Problem)
	assert.Empty(t, s.Problem)
}

func TestCheckNotRouted(t *testing.T) {
	// 192.0.2.1 (TEST-NET-1) never leaves through loopback.
	s := VPN{Interface: loopback(t), Host: "192.0.2.1", Port: 22}.Check(context.Background())
	assert.False(t, s.Routed)
	assert.False(t, s.Connected)
	assert.NotEmpty(t, s.Problem)
}

func TestCheckProbeFails(t *testing.T) {
	s := VPN{Interface: loopback(t), Probe: closedPort(t)}.Check(context.Background())
	assert.False(t, s.ProbeOK)
	assert.False(t, s.Connected)
	assert.Contains(t, s.Problem, "no answer from")
}

func TestSnapshotIncludesVPN(t *testing.T) {
	tr := New("v1")
	assert.Nil(t, tr.Snapshot().VPN)
	tr.SetVPN(VPNState{Interface: "wg0", Connected: true, Addresses: []string{"10.66.1.2"}})
	s := tr.Snapshot()
	require.NotNil(t, s.VPN)
	assert.True(t, s.VPN.Connected)
	assert.Equal(t, []string{"10.66.1.2"}, s.VPN.Addresses)
}
