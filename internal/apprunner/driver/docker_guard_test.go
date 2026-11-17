package driver

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// guardAllowed names one construct that may break one rule: a file, the top-level declaration it sits in, and the
// rule. Everything else in every file, allow-listed files included, is subject to every rule.
type guardAllowed struct{ file, decl, rule string }

// Every Docker socket client comes from newDockerClient, which wraps the allow-list. The check is structural, over
// the syntax (package-level declarations included) of every non-test file under internal/apprunner and
// cmd/app-runner, preflight.go included. The rules are dockerGuardFindings; imports are resolved per file, so an
// alias or a dot import changes nothing. Only the constructs below may break a rule, each for a reason:
//   - driver/docker_client.go newDockerClient: the one Docker client constructor, its transport and client;
//   - driver/socket_linux.go and socket_other.go dockerDialer: the dialer, called only by the constructor;
//   - driver/preflight.go daemonClient: wraps a given transport in the allow-list;
//   - the egress files below: the broker's own reviewed network egress for apps, the app-socket clients (host
//     portal-app.invalid) and the dial to the runner's own control socket. They are exempt from the network rules
//     only, never from dockerDialer, raw connect or file conn, and none may name the Docker host.
//
// A raw unix.Connect or syscall.Connect, and net.FileConn or net.FilePacketConn, are violations everywhere, since
// they would reach the socket without the allow-list.
var guardAllowList = []guardAllowed{
	{"driver/docker_client.go", "newDockerClient", ruleHTTPLiteral},
	{"driver/docker_client.go", "newDockerClient", ruleDockerDialer},
	{"driver/socket_linux.go", "dockerDialer", ruleDockerDialer},
	{"driver/socket_linux.go", "dockerDialer", ruleNetDial},
	{"driver/socket_linux.go", "dockerDialer", ruleMethodDial},
	{"driver/socket_other.go", "dockerDialer", ruleDockerDialer},
	{"driver/preflight.go", "daemonClient", ruleHTTPLiteral},
}

var guardAllowedEgressFiles = map[string]bool{
	"broker/http.go": true, "broker/http_stream.go": true,
	"driver/backend_handshake_linux.go": true, "driver/backend_request_linux.go": true, "driver/candidate_readiness_linux.go": true,
	"driver/service_backend_task_linux.go": true, "transport/transport_linux.go": true,
}

func guardIsExempt(rel string, f guardFinding) bool {
	for _, a := range guardAllowList {
		if a.file == rel && a.decl == f.decl && a.rule == f.rule {
			return true
		}
	}
	if guardAllowedEgressFiles[rel] {
		switch f.rule {
		case ruleNetDial, ruleMethodDial, ruleHTTPLiteral, ruleHTTPNew, ruleHTTPDefault:
			return true
		}
	}
	return false
}

func TestOnlyTheDockerClientConstructorDialsTheSocket(t *testing.T) {
	fset := token.NewFileSet()
	scanned := 0
	sawPreflight := false
	for _, root := range []string{"..", "../../../cmd/app-runner"} {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			file, err := parser.ParseFile(fset, path, raw, 0)
			if err != nil {
				return err
			}
			scanned++
			rel := strings.TrimPrefix(filepath.ToSlash(path), "../")
			sawPreflight = sawPreflight || rel == "driver/preflight.go"
			for _, f := range dockerGuardFindings(fset, file) {
				if !guardIsExempt(rel, f) {
					t.Errorf("%s: %s (in %s, rule %s)", fset.Position(f.pos), f.text, f.decl, f.rule)
				}
			}
			if guardAllowedEgressFiles[rel] && strings.Contains(string(raw), "docker.invalid") {
				t.Errorf("%s is allow-listed for non-Docker traffic yet names the Docker host", rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned < 100 {
		t.Fatalf("guard scanned only %d files", scanned)
	}
	if !sawPreflight {
		t.Fatal("guard did not walk driver/preflight.go")
	}
}

// The allow-list must not rot: every entry must still match a finding, otherwise it exempts nothing and hides a
// rename.
func TestDockerGuardAllowListEntriesAreAllUsed(t *testing.T) {
	fset := token.NewFileSet()
	used := map[guardAllowed]bool{}
	for _, a := range guardAllowList {
		file, err := parser.ParseFile(fset, filepath.Join("..", a.file), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range dockerGuardFindings(fset, file) {
			if f.decl == a.decl && f.rule == a.rule {
				used[a] = true
			}
		}
	}
	for _, a := range guardAllowList {
		if !used[a] {
			t.Errorf("allow-list entry %+v matches no finding", a)
		}
	}
}

// The guard itself must see violations: the same matcher runs over source that breaks each rule, including the
// alias, dot-import, raw-connect and file-conn bypasses.
func TestDockerGuardRulesDetectViolations(t *testing.T) {
	cases := map[string]struct{ src, rule string }{
		"dial":               {`package x; import "net"; var _, _ = net.Dial("tcp", "a")`, ruleNetDial},
		"dialer":             {`package x; import "net"; var d = net.Dialer{}`, ruleNetDial},
		"method dial":        {`package x; func f(d interface{ DialContext() }) { d.DialContext() }`, ruleMethodDial},
		"transport":          {`package x; import "net/http"; var t = &http.Transport{}`, ruleHTTPLiteral},
		"client":             {`package x; import "net/http"; var c = &http.Client{}`, ruleHTTPLiteral},
		"new":                {`package x; import "net/http"; var t = new(http.Transport)`, ruleHTTPNew},
		"default":            {`package x; import "net/http"; var c = http.DefaultClient`, ruleHTTPDefault},
		"default transport":  {`package x; import "net/http"; var c = http.DefaultTransport`, ruleHTTPDefault},
		"dockerdial":         {`package x; var d = dockerDialer("s")`, ruleDockerDialer},
		"aliased http":       {`package x; import h "net/http"; var c = &h.Client{}`, ruleHTTPLiteral},
		"aliased http new":   {`package x; import h "net/http"; var c = new(h.Transport)`, ruleHTTPNew},
		"aliased net":        {`package x; import nn "net"; var _, _ = nn.Dial("unix", "a")`, ruleNetDial},
		"aliased net dialer": {`package x; import nn "net"; var d = nn.Dialer{}`, ruleNetDial},
		"dot http":           {`package x; import . "net/http"; var c = &Client{}`, ruleHTTPLiteral},
		"dot http default":   {`package x; import . "net/http"; var c = DefaultClient`, ruleHTTPDefault},
		"dot net":            {`package x; import . "net"; var _, _ = Dial("unix", "a")`, ruleNetDial},
		"unix connect":       {`package x; import "golang.org/x/sys/unix"; func f(fd int) { _ = unix.Connect(fd, nil) }`, ruleRawConnect},
		"aliased unix":       {`package x; import u "golang.org/x/sys/unix"; func f(fd int) { _ = u.Connect(fd, nil) }`, ruleRawConnect},
		"dot unix":           {`package x; import . "golang.org/x/sys/unix"; func f(fd int) { _ = Connect(fd, nil) }`, ruleRawConnect},
		"syscall connect":    {`package x; import "syscall"; func f(fd int) { _ = syscall.Connect(fd, nil) }`, ruleRawConnect},
		"file conn":          {`package x; import "net"; func f() { net.FileConn(nil) }`, ruleFileConn},
		"file packet conn":   {`package x; import "net"; func f() { net.FilePacketConn(nil) }`, ruleFileConn},
		"aliased file conn":  {`package x; import n "net"; func f() { n.FileConn(nil) }`, ruleFileConn},
		"dot file conn":      {`package x; import . "net"; func f() { FileConn(nil) }`, ruleFileConn},
	}
	for name, c := range cases {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "x.go", c.src, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		hit := false
		for _, finding := range dockerGuardFindings(fset, f) {
			hit = hit || finding.rule == c.rule
		}
		if !hit {
			t.Errorf("%s: rule %s does not fire", name, c.rule)
		}
	}
}

// An allow-listed file is not skipped: an allow-listed construct stays quiet, a second violation in the same file
// outside it does not.
func TestDockerGuardAllowListedFileStillFailsOutsideItsConstruct(t *testing.T) {
	src := `package driver
import "net/http"
func newDockerClient() *http.Client { return &http.Client{} }
func other() *http.Client { return &http.Client{} }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var unexempt []guardFinding
	for _, finding := range dockerGuardFindings(fset, f) {
		if !guardIsExempt("driver/docker_client.go", finding) {
			unexempt = append(unexempt, finding)
		}
	}
	if len(unexempt) != 1 || unexempt[0].decl != "other" {
		t.Fatalf("want exactly the violation in other(), got %+v", unexempt)
	}
}

// Look-alikes stay quiet, otherwise the guard would be tuned out: a local called net, an unrelated package with a
// Connect function or an http.Client, and a listener.
func TestDockerGuardRulesIgnoreLookAlikes(t *testing.T) {
	for name, src := range map[string]string{
		"local net":      `package x; func f() { net := struct{ Dialer int }{}; _ = net.Dialer }`,
		"other connect":  `package x; import "example.com/y/unix"; func f() { _ = unix.Connect() }`,
		"other http":     `package x; import "example.com/y/http"; var c = &http.Client{}`,
		"net listen":     `package x; import "net"; var _, _ = net.Listen("tcp", "a")`,
		"renamed to net": `package x; import net "example.com/y/net"; var _ = net.DialTimeout`,
	} {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "x.go", src, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := dockerGuardFindings(fset, f); len(got) != 0 {
			t.Errorf("%s: unexpected findings %+v", name, got)
		}
	}
}
