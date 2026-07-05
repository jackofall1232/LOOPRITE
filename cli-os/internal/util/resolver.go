// resolver.go installs a custom net.DefaultResolver when the operator names DNS servers via
// LOOPRITE_DNS, or — Android only — when the platform gives Go's pure resolver nothing to read.
// See docs/android-architecture.md §4 G1: Go's DNS resolver falls back to a hardcoded
// localhost:53 when it can't find /etc/resolv.conf (stock Android ships none), which would make
// every outbound provider call fail DNS resolution before it starts. Desktop is unaffected by
// design: LOOPRITE_DNS is normally unset and /etc/resolv.conf exists there, so InitResolver is a
// no-op and net.DefaultResolver is never touched.
package util

import (
	"context"
	"net"
	"os"
	"runtime"
	"strings"
	"time"
)

// dnsDialTimeout bounds one DNS-server dial attempt so a dead/unreachable server fails fast and
// the next configured server (if any) still gets tried within the same resolver call.
const dnsDialTimeout = 5 * time.Second

// androidFallbackDNSServers are the public resolvers used on Android when no resolv.conf exists
// and the operator gave no explicit LOOPRITE_DNS. A var (not an inline literal) so androidFallback
// is independently testable without needing to run on Android hardware.
var androidFallbackDNSServers = []string{"8.8.8.8:53", "1.1.1.1:53"}

// InitResolver installs the custom resolver if warranted. Call it once, at the very top of
// main(), before anything (including config.Load) might perform a DNS lookup — there must be no
// window where a lookup runs against the default resolver this function was about to replace.
func InitResolver() {
	if servers := parseDNSServers(os.Getenv("LOOPRITE_DNS")); len(servers) > 0 {
		installResolver(servers)
		return
	}
	if servers := androidFallback(runtime.GOOS, "/etc/resolv.conf"); len(servers) > 0 {
		installResolver(servers)
	}
}

// androidFallback returns the public-DNS fallback servers when goos is "android" and
// resolvConfPath does not exist, else nil. Split out from InitResolver so the decision is
// testable on any host (goos/path are parameters, not read from the live environment here).
func androidFallback(goos, resolvConfPath string) []string {
	if goos != "android" {
		return nil
	}
	if _, err := os.Stat(resolvConfPath); err == nil {
		return nil // resolv.conf exists — trust it like every other platform does
	}
	return androidFallbackDNSServers
}

// parseDNSServers splits a LOOPRITE_DNS value ("ip[:port],ip[:port],...") into dial addresses,
// defaulting a bare IP to port 53. An unparseable entry is skipped rather than failing the whole
// list — one typo must not take down every configured server — and "no valid entries at all"
// returns nil so the caller leaves the resolver untouched instead of installing an empty dialer.
func parseDNSServers(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if addr, ok := normalizeDNSAddr(part); ok {
			out = append(out, addr)
		}
	}
	return out
}

// normalizeDNSAddr validates s as an IP (optionally "ip:port") and returns a "host:port" dial
// address, defaulting to port 53 for a bare IP (v4 or v6).
func normalizeDNSAddr(s string) (string, bool) {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		// The common case ("8.8.8.8", "::1") has no port for SplitHostPort to find — treat the
		// whole string as the host and default the port instead of rejecting it.
		host, port = s, "53"
	}
	if net.ParseIP(host) == nil {
		return "", false
	}
	return net.JoinHostPort(host, port), true
}

// installResolver wires net.DefaultResolver to a dialer that tries servers in a fixed order,
// first successful dial wins. Go's resolver may invoke Dial more than once per lookup (retries,
// separate A/AAAA questions); always starting from the same server is the simplest deterministic
// policy, and a genuinely dead server fails its own dial quickly (dnsDialTimeout) rather than
// hanging the whole lookup.
func installResolver(servers []string) {
	dialer := &net.Dialer{Timeout: dnsDialTimeout}
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var lastErr error
			for _, s := range servers {
				// network is exactly what Go's resolver asked for ("udp" or a "tcp" retry after
				// truncation) — honor it rather than hardcoding one transport.
				conn, err := dialer.DialContext(ctx, network, s)
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}
}
