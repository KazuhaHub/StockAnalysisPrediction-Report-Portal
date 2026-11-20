package hostsetup

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/provision"
)

const fstabBackup = "/etc/fstab.before-portal-app-runner"

// requiredFstabOptions are what a volume line must carry to count as the line host-setup would add. Extra
// options (nofail, X-fstrim.notrim, an operator's own) are the operator's and do not conflict.
var requiredFstabOptions = []string{"loop", "prjquota", "nodev", "nosuid", "noexec"}

type fstabLine struct {
	spec, file, fsType string
	options            map[string]bool
	raw                string
}

func parseFstab(data []byte) []fstabLine {
	var lines []fstabLine
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		raw := scanner.Text()
		fields := strings.Fields(raw)
		if len(fields) < 4 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		options := map[string]bool{}
		for _, option := range strings.Split(fields[3], ",") {
			options[option] = true
		}
		lines = append(lines, fstabLine{spec: unescapeFstab(fields[0]), file: unescapeFstab(fields[1]), fsType: fields[2], options: options, raw: raw})
	}
	return lines
}

var fstabOctal = regexp.MustCompile(`\\[0-7]{3}`)

func unescapeFstab(s string) string {
	return fstabOctal.ReplaceAllStringFunc(s, func(m string) string {
		n, _ := strconv.ParseUint(m[1:], 8, 8)
		return string([]byte{byte(n)})
	})
}

// volumeLine is the line host-setup adds, the one provision prints as option A.
func volumeLine(image, mount string) string {
	return fmt.Sprintf("%s %s xfs loop,%s,nofail,X-fstrim.notrim 0 0", image, mount, provision.MountOptions)
}

// fstabState compares the table with the line a volume needs. A line that names the mount point or the image
// with other fields is a conflict; no line is "missing"; the same fields are a match.
func fstabState(lines []fstabLine, image, mount string) (state, detail string) {
	state = "missing"
	for _, l := range lines {
		if l.file != mount && l.spec != image {
			continue
		}
		ok := l.spec == image && l.file == mount && l.fsType == "xfs"
		for _, option := range requiredFstabOptions {
			ok = ok && l.options[option]
		}
		if !ok {
			return "conflict", "an fstab line names " + mount + " or " + image + " with other fields: " + l.raw
		}
		state = "match"
	}
	return state, ""
}

var findmntSummary = regexp.MustCompile(`(\d+) parse errors?, (\d+) errors?, (\d+) warnings?`)

// findmntAccepts decides a `findmnt --verify` result. Warnings alone are not a failure; any error is, and so is
// an unreadable result.
func findmntAccepts(out string, err error) bool {
	if err == nil {
		return true
	}
	m := findmntSummary.FindStringSubmatch(out)
	return m != nil && m[1] == "0" && m[2] == "0"
}

// appendFstab adds the missing lines. It writes a temporary file beside /etc/fstab, checks it with
// `findmnt --verify --tab-file`, fsyncs it and renames it over the file. The first time only, it keeps
// /etc/fstab.before-portal-app-runner. A failed check leaves /etc/fstab untouched.
func (r *run) appendFstab(ctx context.Context, wanted [][2]string) error {
	path := r.env.path(fstabPath)
	current, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	info, statErr := os.Stat(path)
	mode := os.FileMode(0o644)
	if statErr == nil {
		mode = info.Mode().Perm()
	}
	lines := parseFstab(current)
	var add []string
	for _, w := range wanted {
		state, detail := fstabState(lines, w[0], w[1])
		switch state {
		case "conflict":
			return refuse("%s", detail)
		case "missing":
			add = append(add, volumeLine(w[0], w[1]))
		}
	}
	if len(add) == 0 {
		return nil
	}
	next := append([]byte{}, current...)
	if len(next) > 0 && next[len(next)-1] != '\n' {
		next = append(next, '\n')
	}
	next = append(next, []byte("# Loop-mounted XFS volumes of the Portal app-runner (added by host-setup)\n"+strings.Join(add, "\n")+"\n")...)

	temp := path + ".portal-app-runner.new"
	os.Remove(temp)
	file, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	cleanup := func() { os.Remove(temp) }
	if _, err := file.Write(next); err != nil {
		file.Close()
		cleanup()
		return err
	}
	if err := file.Chmod(mode); err != nil {
		file.Close()
		cleanup()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		cleanup()
		return err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return err
	}
	// The logical path: the tool runs on the host, where Prefix is empty.
	logical := fstabPath + ".portal-app-runner.new"
	if out, err := r.env.Provision.Exec.Run(ctx, "findmnt", "--verify", "--tab-file", logical); !findmntAccepts(out, err) {
		cleanup()
		return fmt.Errorf("findmnt --verify rejected the new fstab; /etc/fstab is unchanged: %v", err)
	}
	backup := r.env.path(fstabBackup)
	if _, err := os.Lstat(backup); os.IsNotExist(err) && statErr == nil {
		if err := os.WriteFile(backup, current, 0o644); err != nil {
			cleanup()
			return fmt.Errorf("keep the backup of fstab: %w", err)
		}
	}
	if err := os.Rename(temp, path); err != nil {
		cleanup()
		return err
	}
	syncDir(filepath.Dir(path))
	r.fstabChanged = true
	return nil
}

func syncDir(path string) {
	if dir, err := os.Open(path); err == nil {
		dir.Sync()
		dir.Close()
	}
}
