package simulator

import (
	"context"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

const gatewayLookupTimeout = time.Second

func (a *ActiveCall) runFromCallee(ctx context.Context, callee string) string {
	if !strings.HasPrefix(callee, "sip:") {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(callee, "sip:"), "@")
	if len(parts) != 2 || !activeCallRunID.MatchString(parts[0]) {
		return ""
	}
	host, ok := sipAuthorityHost(parts[1])
	if !ok {
		return ""
	}
	// Preserve the configured logical domain's original exact host/port match.
	if strings.EqualFold(parts[1], a.opts.SIPDomain) {
		return parts[0]
	}
	gatewayHost := strings.ToLower(strings.TrimSuffix(a.controlURL.Hostname(), "."))
	if host == gatewayHost {
		return parts[0]
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		// Never resolve a hostname obtained from the incoming webhook.
		return ""
	}
	if gatewayIP, err := netip.ParseAddr(gatewayHost); err == nil {
		if ip.Unmap() == gatewayIP.Unmap() {
			return parts[0]
		}
		return ""
	}
	// Resolve at admission, not startup: Docker DNS can become available after
	// this process starts. A failed lookup rejects this invite without changing
	// subsequent lookups or retaining an old address after gateway replacement.
	timeout := gatewayLookupTimeout
	if a.opts.DialTimeout < timeout {
		timeout = a.opts.DialTimeout
	}
	lookupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	addresses, err := a.lookupIPAddr(lookupCtx, gatewayHost)
	if err != nil || lookupCtx.Err() != nil {
		return ""
	}
	for _, address := range addresses {
		resolved, ok := netip.AddrFromSlice(address.IP)
		if ok && address.Zone == "" && ip.Unmap() == resolved.Unmap() {
			return parts[0]
		}
	}
	return ""
}

// sipAuthorityHost accepts a bare DNS name/IPv4 or bracketed IPv6, optionally
// followed by a numeric port. URI parameters, userinfo, escapes and zones are
// deliberately unsupported, so they cannot broaden destination matching.
func sipAuthorityHost(authority string) (string, bool) {
	if authority == "" || len(authority) > 260 {
		return "", false
	}
	host, port := authority, ""
	hasPort := false
	if strings.HasPrefix(authority, "[") {
		end := strings.IndexByte(authority, ']')
		if end < 0 {
			return "", false
		}
		host = authority[1:end]
		if end+1 < len(authority) {
			if authority[end+1] != ':' {
				return "", false
			}
			port, hasPort = authority[end+2:], true
		}
		ip, err := netip.ParseAddr(host)
		if err != nil || !ip.Is6() || ip.Zone() != "" {
			return "", false
		}
		host = ip.String()
	} else {
		if strings.Count(authority, ":") > 1 {
			return "", false
		}
		if i := strings.IndexByte(authority, ':'); i >= 0 {
			host, port, hasPort = authority[:i], authority[i+1:], true
		}
		if ip, err := netip.ParseAddr(host); err == nil {
			host = ip.String()
		} else if !validSIPHostname(host) {
			return "", false
		}
	}
	if hasPort {
		if port == "" || len(port) > 5 {
			return "", false
		}
		for _, c := range port {
			if c < '0' || c > '9' {
				return "", false
			}
		}
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", false
		}
	}
	return strings.ToLower(strings.TrimSuffix(host, ".")), true
}

func validSIPHostname(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
