package provision

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// parseLoops reads `losetup --json --list` output and returns the devices backed by image. Empty output means
// no loop device is attached.
func parseLoops(output, image string) ([]string, error) {
	if strings.TrimSpace(output) == "" {
		return nil, nil
	}
	var list struct {
		Loopdevices []struct {
			Name     string `json:"name"`
			BackFile string `json:"back-file"`
		} `json:"loopdevices"`
	}
	if err := json.Unmarshal([]byte(output), &list); err != nil {
		return nil, fmt.Errorf("unreadable losetup output")
	}
	var names []string
	for _, d := range list.Loopdevices {
		if d.BackFile == image {
			names = append(names, d.Name)
		}
	}
	return names, nil
}

var octalEscape = regexp.MustCompile(`\\[0-7]{3}`)

func unescapeMountField(s string) string {
	return octalEscape.ReplaceAllStringFunc(s, func(m string) string {
		n, _ := strconv.ParseUint(m[1:], 8, 8)
		return string([]byte{byte(n)})
	})
}

// ParseMountInfo reads /proc/self/mountinfo. The returned options are the union of the per-mount and the
// per-superblock options, which is where nodev/nosuid/noexec and prjquota respectively appear.
func ParseMountInfo(r io.Reader) ([]Mount, error) {
	var mounts []Mount
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		sep := -1
		for i, f := range fields {
			if f == "-" {
				sep = i
				break
			}
		}
		if sep < 6 || len(fields) < sep+4 {
			return nil, fmt.Errorf("unreadable mount table line")
		}
		options := map[string]bool{}
		for _, o := range strings.Split(fields[5]+","+fields[sep+3], ",") {
			if o != "" {
				options[o] = true
			}
		}
		mounts = append(mounts, Mount{
			Point: unescapeMountField(fields[4]), FSType: fields[sep+1], Source: unescapeMountField(fields[sep+2]), Options: options,
		})
	}
	return mounts, scanner.Err()
}

// UnitName is the systemd mount unit name for a mount point, as `systemd-escape -p --suffix=mount` makes it.
func UnitName(path string) string {
	p := strings.Trim(path, "/")
	if p == "" {
		return "-.mount"
	}
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '/':
			b.WriteByte('-')
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == ':', c == '_', c == '.' && i > 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, `\x%02x`, c)
		}
	}
	return b.String() + ".mount"
}

// RecommendedDiskBytes is the largest whole-GiB capacity.disk_bytes the two volumes can honour: the data volume
// must hold it and the checkpoint volume must hold two thirds of it (the runner's own capacity rule). It is zero
// when either volume is smaller than 1 GiB.
func RecommendedDiskBytes(dataUsable, checkpointUsable int64) int64 {
	limit := min(dataUsable, checkpointUsable/2*3)
	return limit / gib * gib
}

func render(out io.Writer, spec Spec, results []VolumeResult) {
	var data, checkpoint VolumeResult
	for _, r := range results {
		if r.Name == "data" {
			data = r
		} else {
			checkpoint = r
		}
	}
	fmt.Fprintf(out, "\nRunner configuration (service.json):\n  \"data_root\": %q,\n  \"checkpoint_root\": %q,\n", data.RootDir, checkpoint.RootDir)
	recommended := RecommendedDiskBytes(data.UsableBytes, checkpoint.UsableBytes)
	fmt.Fprintf(out, "  \"capacity\": {\"disk_bytes\": %d}\n", recommended)
	fmt.Fprintf(out, "The runner is not ready when capacity.disk_bytes (default 8 GiB) exceeds the data volume's usable size, or\n"+
		"when the checkpoint volume is smaller than two thirds of it. XFS reserves part of an image for its log, so an\n"+
		"8 GiB image needs the lower value above. Usable: data %d bytes, checkpoint %d bytes.\n", data.UsableBytes, checkpoint.UsableBytes)
	fmt.Fprintf(out, "\nPersist the mounts after a reboot with ONE of the following (nothing under /etc was changed).\n\n")
	fmt.Fprintf(out, "A. /etc/fstab\n")
	for _, r := range []VolumeResult{data, checkpoint} {
		fmt.Fprintf(out, "%s %s xfs loop,%s 0 0\n", r.Image, r.Mount, MountOptions)
	}
	fmt.Fprintf(out, "\nB. systemd mount units\n")
	for _, r := range []VolumeResult{data, checkpoint} {
		fmt.Fprintf(out, "# /etc/systemd/system/%s\n[Unit]\nDescription=Portal app-runner %s volume\n[Mount]\nWhat=%s\nWhere=%s\nType=xfs\nOptions=loop,%s\n[Install]\nWantedBy=local-fs.target\n\n",
			UnitName(r.Mount), r.Name, r.Image, r.Mount, MountOptions)
	}
	fmt.Fprintf(out, "Order the runner after both volumes with a drop-in:\n"+
		"# /etc/systemd/system/portal-app-runner.service.d/xfs-images.conf\n[Unit]\nRequiresMountsFor=%s %s\n\n", data.Mount, checkpoint.Mount)
	fmt.Fprintf(out, "After a reboot mount(8) attaches the loop device itself, probably without --direct-io (verify on the host); that\n"+
		"changes performance only, not the quota or the journal ordering.\n"+
		"Never copy a live image as a backup, and exclude both image files from host backups (handbook).\n")
}
