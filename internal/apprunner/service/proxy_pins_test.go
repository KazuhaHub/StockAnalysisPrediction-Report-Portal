package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func quotesPin() appplatform.ProxyPin {
	return appplatform.ProxyPin{Host: "quotes.internal.anchan.kazuhahub.com", Address: "127.0.0.1", Port: 20443}
}

// proxy_pins is read from the private configuration, and absent means empty. Fails closed: any entry the
// policy side would refuse stops the runner at start.
func TestConfigProxyPins(t *testing.T) {
	dir := canonicalTempDir(t)
	path := filepath.Join(dir, "service.json")

	cfg := configFixture(dir)
	writePrivate(t, path, cfg)
	loaded, err := LoadConfig(path)
	if err != nil || len(loaded.ProxyPins) != 0 {
		t.Fatalf("absent proxy_pins: %+v %v", loaded.ProxyPins, err)
	}

	cfg.ProxyPins = []appplatform.ProxyPin{quotesPin()}
	writePrivate(t, path, cfg)
	loaded, err = LoadConfig(path)
	if err != nil || !reflect.DeepEqual(loaded.ProxyPins, cfg.ProxyPins) {
		t.Fatalf("one entry: %+v %v", loaded.ProxyPins, err)
	}

	// The documented spelling loads.
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"proxy_pins":[{"host":"quotes.internal.anchan.kazuhahub.com","address":"127.0.0.1","port":20443}]`) {
		t.Fatalf("encoding: %s", raw)
	}

	long := func(n int) []appplatform.ProxyPin {
		var out []appplatform.ProxyPin
		for i := 0; i < n; i++ {
			out = append(out, appplatform.ProxyPin{Host: string(rune('a'+i)) + ".example.com", Address: "127.0.0.1", Port: 443})
		}
		return out
	}
	for name, pins := range map[string][]appplatform.ProxyPin{
		"denied address":     {{Host: "a.example.com", Address: "169.254.169.254", Port: 443}},
		"metadata":           {{Host: "a.example.com", Address: "100.100.100.200", Port: 443}},
		"wildcard host":      {{Host: "*.example.com", Address: "127.0.0.1", Port: 443}},
		"port zero":          {{Host: "a.example.com", Address: "127.0.0.1", Port: 0}},
		"port 65536":         {{Host: "a.example.com", Address: "127.0.0.1", Port: 65536}},
		"mapped address":     {{Host: "a.example.com", Address: "::ffff:127.0.0.1", Port: 443}},
		"duplicate":          {quotesPin(), quotesPin()},
		"unsorted":           {{Host: "b.example.com", Address: "127.0.0.1", Port: 443}, {Host: "a.example.com", Address: "127.0.0.1", Port: 443}},
		"more than 16":       long(17),
		"prefix, not an ip":  {{Host: "a.example.com", Address: "127.0.0.0/8", Port: 443}},
		"unresolvable label": {{Host: "a.example.com", Address: "localhost", Port: 443}},
	} {
		t.Run(name, func(t *testing.T) {
			c := configFixture(dir)
			c.ProxyPins = pins
			writePrivate(t, path, c)
			if _, err := LoadConfig(path); err == nil {
				t.Fatal("an invalid proxy_pins block was accepted")
			}
		})
	}
	// Exactly 16 sorted entries load.
	c := configFixture(dir)
	c.ProxyPins = long(16)
	writePrivate(t, path, c)
	if _, err := LoadConfig(path); err != nil {
		t.Fatalf("16 entries: %v", err)
	}
	// An unknown member of an entry is refused by the strict decoder.
	for name, body := range map[string]string{
		"extra member": `"proxy_pins":[{"host":"a.example.com","address":"127.0.0.1","port":443,"note":"x"}]`,
		"upper case":   `"proxy_pins":[{"Host":"a.example.com","address":"127.0.0.1","port":443}]`,
		"duplicate":    `"proxy_pins":[{"host":"a.example.com","host":"b.example.com","address":"127.0.0.1","port":443}]`,
		"string port":  `"proxy_pins":[{"host":"a.example.com","address":"127.0.0.1","port":"443"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			writePrivate(t, path, configFixture(dir))
			text, _ := os.ReadFile(path)
			edited := strings.TrimSuffix(string(text), "}") + "," + body + "}"
			if err := os.WriteFile(path, []byte(edited), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(path); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// A configuration file that is not 0600 or not owned by the effective user is refused with pins in it, as
// without; the Compose recipe's read-only bind mount of a root-owned 0600 file passes. Fails closed.
func TestConfigWithPinsKeepsThePrivateFileRules(t *testing.T) {
	dir := canonicalTempDir(t)
	path := filepath.Join(dir, "service.json")
	cfg := configFixture(dir)
	cfg.ProxyPins = []appplatform.ProxyPin{quotesPin()}
	writePrivate(t, path, cfg)
	for _, mode := range []os.FileMode{0640, 0604, 0644, 0660} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Fatalf("mode %o accepted", mode)
		}
	}
	// 0400 is what a read-only mount of the file looks like to the runner: still accepted.
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err != nil {
		t.Fatalf("a read-only 0400 file: %v", err)
	}
	// A symlinked directory is refused.
	link := filepath.Join(canonicalTempDir(t), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(filepath.Join(link, "service.json")); err == nil {
		t.Fatal("a configuration reached through a symlinked directory was accepted")
	}
}

// With pins, the daemon must be identified in the runner's network namespace, or the runner refuses to start
// with proxy_pins_netns. Without pins the check is not even asked. Fails closed.
func TestProxyPinsRequireTheHostNetworkNamespace(t *testing.T) {
	pins := []appplatform.ProxyPin{quotesPin()}
	asked := 0
	pass := func(context.Context) error { asked++; return nil }
	fail := func(context.Context) error { asked++; return errors.New("another namespace") }

	if err := CheckProxyPinsNamespace(context.Background(), nil, fail); err != nil || asked != 0 {
		t.Fatalf("no pins: %v, asked %d", err, asked)
	}
	if err := CheckProxyPinsNamespace(context.Background(), []appplatform.ProxyPin{}, fail); err != nil || asked != 0 {
		t.Fatalf("empty pins: %v, asked %d", err, asked)
	}
	if err := CheckProxyPinsNamespace(context.Background(), pins, pass); err != nil || asked != 1 {
		t.Fatalf("pass: %v, asked %d", err, asked)
	}
	if err := CheckProxyPinsNamespace(context.Background(), pins, fail); !errors.Is(err, ErrProxyPinsNetns) || err.Error() != "proxy_pins_netns" {
		t.Fatalf("fail: %v", err)
	}
	if err := CheckProxyPinsNamespace(context.Background(), pins, nil); !errors.Is(err, ErrProxyPinsNetns) {
		t.Fatalf("no check available must refuse: %v", err)
	}
}
