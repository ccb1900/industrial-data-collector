// Package websafety holds the process-level policy for the collector's
// management HTTP surface. The console controls collection triggers, plugin
// lifecycle and configuration — exposing it on every interface by default is
// a security boundary mistake, not a UI detail.
package websafety

import (
	"errors"
	"net"
	"strings"
)

// ErrNonLoopback is returned when an operator asks to bind a non-loopback
// address without an explicit acknowledgement. "Safe by default" means the
// default deployment cannot be reached from the plant network.
var ErrNonLoopback = errors.New("refusing to bind a non-loopback address without --allow-remote")

// ValidateBind checks the requested listen address. Loopback addresses (and
// the empty address used by net/http tests) are always allowed. Everything
// else requires explicit opt-in from an operator who has arranged network
// isolation and authentication at the reverse proxy.
func ValidateBind(addr string, allowRemote bool) error {
	if allowRemote {
		return nil
	}
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	} else if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i] // ":8080" / "127.0.0.1:8080" without SplitHostPort edge cases
	}
	// A leading ":" (":8080") means ALL interfaces — net.SplitHostPort
	// returns an empty host for it; never treat that as loopback.
	if host == "" {
		if strings.HasPrefix(strings.TrimSpace(addr), ":") {
			return ErrNonLoopback
		}
		return nil // hostless address in a unit test
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip != nil && ip.IsLoopback() {
		return nil
	}
	return ErrNonLoopback
}
