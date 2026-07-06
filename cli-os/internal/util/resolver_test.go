package util

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestInitResolverInstallsCustomDialer proves LOOPRITE_DNS actually redirects lookups: the
// installed dialer must connect to the configured "server" address, not perform a real DNS
// lookup (kept hermetic with a local UDP listener standing in for a DNS server).
func TestInitResolverInstallsCustomDialer(t *testing.T) {
	orig := net.DefaultResolver
	t.Cleanup(func() { net.DefaultResolver = orig })

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()
	fakeAddr := pc.LocalAddr().String()

	t.Setenv("LOOPRITE_DNS", fakeAddr)
	InitResolver()

	if net.DefaultResolver == orig {
		t.Fatal("InitResolver did not replace net.DefaultResolver")
	}
	if !net.DefaultResolver.PreferGo {
		t.Fatal("installed resolver must set PreferGo")
	}
	if net.DefaultResolver.Dial == nil {
		t.Fatal("installed resolver must set a custom Dial")
	}

	conn, err := net.DefaultResolver.Dial(context.Background(), "udp", "ignored:53")
	if err != nil {
		t.Fatalf("dial via installed resolver: %v", err)
	}
	defer conn.Close()
	if conn.RemoteAddr().String() != fakeAddr {
		t.Fatalf("dial went to %q, want configured DNS server %q", conn.RemoteAddr().String(), fakeAddr)
	}
}

// TestInitResolverMultipleServersFallsThrough proves a dead first server does not sink the whole
// lookup: the dialer must fall through to the next configured server. Uses "tcp" throughout —
// UDP's connect() rarely fails synchronously even against a closed port (connectionless, no
// handshake), so it cannot deterministically exercise the fallthrough branch; TCP against a
// closed port reliably refuses the connection.
func TestInitResolverMultipleServersFallsThrough(t *testing.T) {
	orig := net.DefaultResolver
	t.Cleanup(func() { net.DefaultResolver = orig })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	good := ln.Addr().String()

	deadLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen (for a port to close): %v", err)
	}
	dead := deadLn.Addr().String()
	deadLn.Close() // nothing listens here now; connections to it are refused

	t.Setenv("LOOPRITE_DNS", dead+","+good)
	InitResolver()

	conn, err := net.DefaultResolver.Dial(context.Background(), "tcp", "ignored:53")
	if err != nil {
		t.Fatalf("expected fallthrough to the second (good) server, got error: %v", err)
	}
	defer conn.Close()
	if conn.RemoteAddr().String() != good {
		t.Fatalf("dial went to %q, want fallthrough server %q", conn.RemoteAddr().String(), good)
	}
}

func TestInitResolverSkipsInvalidEntriesEntirely(t *testing.T) {
	orig := net.DefaultResolver
	t.Cleanup(func() { net.DefaultResolver = orig })

	t.Setenv("LOOPRITE_DNS", "not-an-ip, , also-not-an-ip")
	InitResolver()
	if net.DefaultResolver != orig {
		t.Fatal("an all-invalid LOOPRITE_DNS must leave the resolver untouched")
	}
}

func TestInitResolverUnsetIsNoopOffAndroid(t *testing.T) {
	if runtime.GOOS == "android" {
		t.Skip("this test asserts desktop no-op behavior")
	}
	orig := net.DefaultResolver
	t.Cleanup(func() { net.DefaultResolver = orig })
	t.Setenv("LOOPRITE_DNS", "")
	InitResolver()
	if net.DefaultResolver != orig {
		t.Fatal("unset LOOPRITE_DNS on a non-android host must leave the resolver untouched")
	}
}

func TestAndroidFallback(t *testing.T) {
	if got := androidFallback("linux", "/nonexistent-resolv-conf-for-test"); got != nil {
		t.Fatalf("a non-android goos must never fall back, got %v", got)
	}

	tmp := t.TempDir()
	existing := filepath.Join(tmp, "resolv.conf")
	if err := os.WriteFile(existing, []byte("nameserver 127.0.0.53\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := androidFallback("android", existing); got != nil {
		t.Fatalf("android WITH a resolv.conf must not fall back, got %v", got)
	}

	missing := filepath.Join(tmp, "missing-resolv.conf")
	got := androidFallback("android", missing)
	if len(got) != 2 || got[0] != "8.8.8.8:53" || got[1] != "1.1.1.1:53" {
		t.Fatalf("android with no resolv.conf must fall back to 8.8.8.8,1.1.1.1, got %v", got)
	}

	// A stat failure that is NOT "does not exist" (permission denied, a transient IO error on an
	// unusual Android build, ...) must never be treated as "missing" — falling back in that case
	// would override an operator's real DNS configuration on nothing more than an unconfirmed
	// guess. Simulate this with an unreadable parent directory rather than a special file, since
	// that's a real os.Stat failure mode distinct from "not exist".
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission-denied stat is not enforceable to test this branch")
	}
	unreadableDir := filepath.Join(tmp, "unreadable")
	if err := os.Mkdir(unreadableDir, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(unreadableDir, 0o755) // let t.TempDir() clean it up
	blocked := filepath.Join(unreadableDir, "resolv.conf")
	if got := androidFallback("android", blocked); got != nil {
		t.Fatalf("a non-ENOENT stat failure must not trigger the fallback (must not assume missing), got %v", got)
	}
}

func TestNormalizeDNSAddr(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"8.8.8.8", "8.8.8.8:53", true},
		{"8.8.8.8:5353", "8.8.8.8:5353", true},
		{"::1", "[::1]:53", true},
		// Android commonly reports link-local IPv6 DNS servers with a zone identifier suffix
		// (e.g. "fe80::1%wlan0"); net.ParseIP alone rejects these, but the zone must survive into
		// the dial address so the resolver reaches the right interface.
		{"fe80::1%wlan0", "[fe80::1%wlan0]:53", true},
		{"[fe80::1%wlan0]:547", "[fe80::1%wlan0]:547", true},
		{"not-an-ip", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := normalizeDNSAddr(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Fatalf("normalizeDNSAddr(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
