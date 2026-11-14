package cpuset

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseAcceptsTheKernelListFormat(t *testing.T) {
	for in, want := range map[string][]int{
		"0":         {0},
		"0-3":       {0, 1, 2, 3},
		"1-3":       {1, 2, 3},
		"0,2-3,5":   {0, 2, 3, 5},
		"0-1,4-5\n": {0, 1, 4, 5},
	} {
		got, err := Parse(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("Parse(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
}

func TestParseRefusesAnythingButAStrictlyAscendingList(t *testing.T) {
	for _, in := range []string{"", "\n", "a", "-1", "3-1", "1,,2", "1,", ",1", "2,1", "1,1", "0-3,2", "1-", "-", "0 1", "00", "01-3", "0-1-2", "99999999999999999999"} {
		if got, err := Parse(in); err == nil {
			t.Fatalf("Parse(%q) = %v, want an error", in, got)
		}
	}
}

func TestFormatIsTheCanonicalMinimalRangeList(t *testing.T) {
	for want, in := range map[string][]int{
		"":        nil,
		"0":       {0},
		"1-3":     {1, 2, 3},
		"2-3,5":   {2, 3, 5},
		"0,2,4":   {0, 2, 4},
		"0-1,3-4": {0, 1, 3, 4},
	} {
		if got := Format(in); got != want {
			t.Fatalf("Format(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestValidIsExactlyTheCanonicalForm(t *testing.T) {
	for _, ok := range []string{"0", "1-3", "2-3,5", "0,2,4"} {
		if !Valid(ok) {
			t.Fatalf("%q refused", ok)
		}
	}
	// Parseable but not minimal: Docker would echo it back, but the runner never writes it.
	for _, bad := range []string{"", "1,2,3", "0-1,2", "1-1", "3-1", "x", "1-3\n", " 1-3"} {
		if Valid(bad) {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestEqualIgnoresRepresentationNotMembership(t *testing.T) {
	if !Equal([]int{1, 2}, []int{1, 2}) || Equal([]int{1, 2}, []int{1, 3}) || Equal([]int{1}, []int{1, 2}) || !Equal(nil, []int{}) {
		t.Fatal("Equal is wrong")
	}
}

func TestReadFileReadsAListAndFailsClosed(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if got, err := ReadFile(write("online", "0-3\n")); err != nil || !reflect.DeepEqual(got, []int{0, 1, 2, 3}) {
		t.Fatalf("ReadFile = %v, %v", got, err)
	}
	for name, body := range map[string]string{"empty": "", "garbage": "online\n", "huge": strings.Repeat("0,", 1<<20)} {
		if got, err := ReadFile(write(name, body)); err == nil {
			t.Fatalf("%s: ReadFile = %v, want an error", name, got)
		}
	}
	if _, err := ReadFile(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a missing file read as a list")
	}
}
