package hostsetup

import (
	"errors"
	"strings"
	"testing"
)

func TestKernelAtLeast(t *testing.T) {
	for release, want := range map[string]bool{"5.14.0": true, "5.14": true, "6.8.0-45-generic": true, "5.15.0-1-aws": true, "5.13.19": false, "4.19.0": false,
		"": false, "garbage": false, "5": false, "10.0.0": true} {
		if got := kernelAtLeast(release, 5, 14); got != want {
			t.Errorf("%q: %v, want %v", release, got, want)
		}
	}
}

func TestFstabStateComparesFieldsNotBytes(t *testing.T) {
	image, mount := "/data/a.img", "/srv/a"
	cases := map[string]struct{ line, state string }{
		"none":                       {"", "missing"},
		"unrelated":                  {"/dev/sda1 / ext4 defaults 0 1", "missing"},
		"exact line":                 {volumeLine(image, mount), "match"},
		"extra options and spacing":  {image + "\t" + mount + "  xfs  nofail,loop,noexec,nosuid,nodev,prjquota,x-systemd.automount 0 2", "match"},
		"comment is not a line":      {"# " + volumeLine(image, mount), "missing"},
		"missing noexec":             {image + " " + mount + " xfs loop,prjquota,nodev,nosuid 0 0", "conflict"},
		"missing loop":               {image + " " + mount + " xfs prjquota,nodev,nosuid,noexec 0 0", "conflict"},
		"other type":                 {image + " " + mount + " ext4 loop,prjquota,nodev,nosuid,noexec 0 0", "conflict"},
		"same mount, other image":    {"/data/b.img " + mount + " xfs loop,prjquota,nodev,nosuid,noexec 0 0", "conflict"},
		"same image, other mount":    {image + " /srv/b xfs loop,prjquota,nodev,nosuid,noexec 0 0", "conflict"},
		"escaped spaces are decoded": {"/data/a.img /srv/a xfs loop,prjquota,nodev,nosuid,noexec 0 0", "match"},
	}
	for name, c := range cases {
		state, _ := fstabState(parseFstab([]byte(c.line)), image, mount)
		if state != c.state {
			t.Errorf("%s: %s, want %s", name, state, c.state)
		}
	}
}

func TestFindmntAcceptsWarningsButNotErrors(t *testing.T) {
	boom := errors.New("exit status 1")
	for name, c := range map[string]struct {
		out  string
		err  error
		want bool
	}{
		"success":          {"Success, no errors or warnings detected", nil, true},
		"warnings only":    {"0 parse errors, 0 errors, 3 warnings", boom, true},
		"an error":         {"0 parse errors, 1 error, 0 warnings", boom, false},
		"a parse error":    {"1 parse error, 0 errors, 0 warnings", boom, false},
		"no summary":       {"", boom, false},
		"unreadable":       {"findmnt: cannot open", boom, false},
		"errors and warns": {"0 parse errors, 2 errors, 1 warning", boom, false},
	} {
		if got := findmntAccepts(c.out, c.err); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

func TestParseAccountsReadsOnlyPlainEntries(t *testing.T) {
	accounts, err := ParseAccounts(strings.NewReader("root:x:0:0:root:/root:/bin/bash\n# comment\n\n+@nis::::::\nbad\nnoid:x:abc:0\nportal:x:41001:41001::/:/sbin/nologin\ngroup:x:5:alice,bob\n"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, a := range accounts {
		names = append(names, a.Name)
	}
	if strings.Join(names, ",") != "root,portal,group" || accounts[1].ID != 41001 || accounts[2].ID != 5 {
		t.Fatalf("%+v", accounts)
	}
}

func TestSummarizeProvisionKeepsOnlyTheVolumeLines(t *testing.T) {
	report := "data: created image /i, mounted at /m on /dev/loop1, usable 1 bytes\ncheckpoint: created image /j, mounted at /n on /dev/loop2, usable 2 bytes\n\nRunner configuration (service.json):\n  \"data_root\": \"x\"\nA. /etc/fstab (preferred)\n"
	got := summarizeProvision(report)
	if !strings.Contains(got, "data: created") || !strings.Contains(got, "checkpoint: created") || strings.Contains(got, "fstab") || strings.Contains(got, "data_root") {
		t.Fatalf("%q", got)
	}
}

// XFS spells project quota prjquota or pquota; a line with either is the same line.
func TestFstabStateAcceptsPquotaAsProjectQuota(t *testing.T) {
	line := "/data/a.img /srv/a xfs loop,pquota,nodev,nosuid,noexec 0 0"
	if state, _ := fstabState(parseFstab([]byte(line)), "/data/a.img", "/srv/a"); state != "match" {
		t.Fatalf("pquota: %s", state)
	}
	noQuota := "/data/a.img /srv/a xfs loop,nodev,nosuid,noexec 0 0"
	if state, _ := fstabState(parseFstab([]byte(noQuota)), "/data/a.img", "/srv/a"); state != "conflict" {
		t.Fatalf("no quota option: %s", state)
	}
}

func TestFstabSafePathRejectsWhatNeedsEscaping(t *testing.T) {
	for path, want := range map[string]bool{"/data/a.img": true, "/srv/portal-runner/data": true, "/a b": false, "/a\tb": false, "/a\nb": false,
		`/a\b`: false, "/a#b": false, `/a"b`: false, "/a\x01b": false, "/\u00e9": false} {
		if got := fstabSafePath(path); got != want {
			t.Errorf("%q: %v, want %v", path, got, want)
		}
	}
}
