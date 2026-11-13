package tui

import (
	"net"
	"strings"
	"testing"
)

// Opening the control panel while `ngodingmcp -d` runs must explain that the
// service holds the port, not show "bind: address already in use".
func TestStartWhileServiceRunsExplainsPortConflict(t *testing.T) {
	isolateConfig(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	m := NewWithPreset("v-test", t.TempDir(), addr, "")
	m.SetServiceProbe(func() bool { return true })
	m.start()
	if m.running {
		t.Fatal("start must fail when the port is taken")
	}
	for _, want := range []string{"background service is already running on " + addr, "http://" + addr + "/", "ngodingmcp stop"} {
		if !strings.Contains(m.errMsg, want) {
			t.Errorf("errMsg = %q, want it to contain %q", m.errMsg, want)
		}
	}
	if strings.Contains(m.errMsg, "address already in use") {
		t.Errorf("errMsg should not show the raw bind error: %q", m.errMsg)
	}

	// Another program on the port: say so, and still point at status.
	m.SetServiceProbe(func() bool { return false })
	m.start()
	if !strings.Contains(m.errMsg, "port "+addr+" is already in use by another program") ||
		!strings.Contains(m.errMsg, "ngodingmcp status") {
		t.Errorf("errMsg = %q", m.errMsg)
	}

	// No probe (non-systemd systems) behaves like "another program".
	m.SetServiceProbe(nil)
	m.start()
	if !strings.Contains(m.errMsg, "already in use by another program") {
		t.Errorf("errMsg without probe = %q", m.errMsg)
	}
}
