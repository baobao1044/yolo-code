// The network policy (File 08 §8.4.4): default-deny — no tool may reach the
// network unless its host is on the sandbox's allowlist, and only tools whose
// metadata declares Permission.Net:true may even attempt it. The real
// per-process isolation (network namespace on Linux, firewall rule on
// Windows) is platform infra wired later; this ticket enforces the policy gate
// the dispatcher consults before Run, so a misbehaving tool never connects
// to an unlisted host.
//
// The allowlist is exact (matched by full host[:port], never by pattern),
// so a wildcard can't broaden a deny (File 08 §8.4.4).

package exec

import (
	"encoding/json"
	"strings"
)

// AllowHost adds a host (e.g. "proxy.golang.org:443") to the network allowlist
// (File 08 §8.4.4 `--allow-net`). The composition root wires this from config
// at startup.
func (s *Sandbox) AllowHost(host string) {
	if s.hosts == nil {
		s.hosts = map[string]bool{}
	}
	s.hosts[host] = true
}

// HostAllowed reports whether host is on the network allowlist (File 08
// §8.4.4). With no allowlist (or a nil sandbox), every host is denied — the
// default-deny posture.
func (s *Sandbox) HostAllowed(host string) bool {
	if s == nil || s.hosts == nil {
		return false
	}
	return s.hosts[host]
}

// HostFromArgs extracts the target host from a Net tool's args (File 08
// §8.4.4): looks for a "host" or "url" key. The host is what the allowlist is
// matched against; a url is reduced to its host[:port]. Returns "" if no host
// is derivable (the caller denies, since an unknown target is unsafe).
func HostFromArgs(args []byte) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil {
		return ""
	}
	if raw, ok := obj["host"]; ok {
		var h string
		if json.Unmarshal(raw, &h) == nil {
			return h
		}
	}
	if raw, ok := obj["url"]; ok {
		var u string
		if json.Unmarshal(raw, &u) == nil {
			return hostOfURL(u)
		}
	}
	return ""
}

// hostOfURL strips scheme/path from a URL string, leaving host[:port]. A
// conservative parser (not net/url, to avoid importing it here): take the
// part after "://" up to the next "/".
func hostOfURL(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.Index(u, "/"); i >= 0 {
		u = u[:i]
	}
	return u
}

// allowNetwork is the dispatcher's net gate (File 08 §8.4.4). It returns nil
// if the tool may proceed, ErrNetworkDenied if a Net:true tool targets an
// unlisted host. A tool that does not declare Net:true is never gated here
// (its network access, if any, is governed by the Bash command classifier,
// File 08 §8.4.3).
func (e *Engine) allowNetwork(tool Tool, call ToolCall) error {
	if e.sandbox == nil {
		// No sandbox wired → no network gate. Unit tests that don't care
		// about networking use this path; the real runtime always wires a
		// sandbox.
		return nil
	}
	if !tool.Metadata().Permission.Net {
		return nil // not a network tool; not gated here
	}
	host := HostFromArgs(call.Args)
	if host == "" {
		return ErrNetworkDenied // unknown target → deny
	}
	if !e.sandbox.HostAllowed(host) {
		return ErrNetworkDenied
	}
	return nil
}
