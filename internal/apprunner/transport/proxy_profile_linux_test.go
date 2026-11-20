//go:build linux

package transport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProxyProfileQuery(t *testing.T) {
	f := newEgrFixture(t)
	path, _ := egressEndpoint(t, f, true)
	profile, err := queryProxyProfile(context.Background(), path, privateProfile(uint32(os.Geteuid())))
	if err != nil {
		t.Fatal(err)
	}
	if profile != NewProxyProfile() || profile.Format != "portal.runner.proxy-profile/1-dev.1" || profile.SessionFormat != "portal.service-egress-session/1-dev.2" ||
		profile.StreamFormat != "PRXY0001" || profile.Tunnels != 16 || profile.RunnerTunnels != 64 || profile.NewPerMinute != 300 || profile.Burst != 50 ||
		profile.IdleSeconds != 60 || profile.LifetimeSeconds != 600 || profile.DailyBytes != 2<<30 || profile.Mbit != 50 {
		t.Fatalf("%+v", profile)
	}
}

func TestProxyProfileCodecIsClosed(t *testing.T) {
	raw, err := encodeProxyProfile(NewProxyProfile())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseProxyProfile(raw); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{``, `{}`, string(raw) + ` `, string(raw[:len(raw)-1]) + `,"extra":1}`, `{"format":"x"}`} {
		if _, err := ParseProxyProfile([]byte(bad)); !errors.Is(err, ErrProxyUnsupported) {
			t.Fatalf("%q accepted", bad)
		}
	}
	// Every limit is compared exactly: a runner with other limits is not one this Portal may use.
	for name, edit := range map[string]func(*ProxyProfile){
		"tunnels":  func(p *ProxyProfile) { p.Tunnels = 17 },
		"runner":   func(p *ProxyProfile) { p.RunnerTunnels = 65 },
		"rate":     func(p *ProxyProfile) { p.NewPerMinute = 301 },
		"burst":    func(p *ProxyProfile) { p.Burst = 51 },
		"idle":     func(p *ProxyProfile) { p.IdleSeconds = 61 },
		"lifetime": func(p *ProxyProfile) { p.LifetimeSeconds = 601 },
		"daily":    func(p *ProxyProfile) { p.DailyBytes = 3 << 30 },
		"mbit":     func(p *ProxyProfile) { p.Mbit = 51 },
		"session":  func(p *ProxyProfile) { p.SessionFormat = "portal.service-egress-session/1-dev.1" },
		"stream":   func(p *ProxyProfile) { p.StreamFormat = "PRXY0002" },
	} {
		p := NewProxyProfile()
		edit(&p)
		if _, err := encodeProxyProfile(p); err == nil {
			t.Errorf("%s: an invalid profile was encoded", name)
		}
	}
}

func TestProxyProfileUnsupportedOnNarrowRunners(t *testing.T) {
	f := newEgrFixture(t)
	// A runner without an egress registry is a runner that predates the proxy: it closes the query.
	path, _ := egressEndpoint(t, f, false)
	if _, err := queryProxyProfile(context.Background(), path, privateProfile(uint32(os.Geteuid()))); !errors.Is(err, ErrProxyUnsupported) {
		t.Fatalf("query on a narrow runner: %v", err)
	}
	plain := filepath.Join(privateDir(t), "plain.sock")
	plainServer, err := Listen(plain, uint32(os.Geteuid()), &recorder{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = plainServer.Serve(ctx) }()
	if _, err := queryProxyProfile(context.Background(), plain, privateProfile(uint32(os.Geteuid()))); !errors.Is(err, ErrProxyUnsupported) {
		t.Fatalf("query on a plain endpoint: %v", err)
	}
}
