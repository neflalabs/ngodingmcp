package tui

import (
	"fmt"

	"ngodingmcp/internal/app"
	"ngodingmcp/internal/config"
)

// startError turns a failed listen into a message the user can act on. The
// common case is the background service (`ngodingmcp -d`) already holding the
// port: the control panel then points at it instead of showing a raw
// "bind: address already in use".
func (m *Model) startError(addr string, err error) string {
	if !app.IsAddrInUse(err) {
		return "cannot start: " + err.Error()
	}
	if addr == "" {
		addr = config.DefaultAddr
	}
	url := app.BaseURLFor(addr) + "/"
	if m.serviceRunning != nil && m.serviceRunning() {
		return fmt.Sprintf("the background service is already running on %s; open %s, "+
			"or run `ngodingmcp stop` first and start again here", addr, url)
	}
	return fmt.Sprintf("port %s is already in use by another program "+
		"(if it is NgodingMCP, open %s or check `ngodingmcp status`); "+
		"stop it or pick another address", addr, url)
}
