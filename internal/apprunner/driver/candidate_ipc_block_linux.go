//go:build linux

package driver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// candidateIPCSysDevBlock is sysfs's index of block devices by major:minor. A variable so a test can supply a tree.
var candidateIPCSysDevBlock = "/sys/dev/block"

const (
	candidateIPCBlockDepth = 16
	candidateIPCBlockNodes = 256
)

var candidateIPCMajMin = regexp.MustCompile(`^[0-9]+:[0-9]+$`)

// candidateIPCBlockKinds are the device kinds whose sysfs backing is fully described (what they are built on is in
// slaves/, partition or loop/backing_file): SCSI/SATA, NVMe, virtio, Xen, device-mapper, MD RAID and loop,
// each with its partitions. Anything else (nbd, ublk, rbd, zram, optical, ...) is backed by something sysfs does
// not describe and is refused.
var candidateIPCBlockKinds = regexp.MustCompile(`^(sd[a-z]+[0-9]*|vd[a-z]+[0-9]*|xvd[a-z]+[0-9]*|nvme[0-9]+n[0-9]+(p[0-9]+)?|dm-[0-9]+|md[0-9]+(p[0-9]+)?|loop[0-9]+(p[0-9]+)?)$`)

// candidateIPCBlockAncestry is what one block device is built on, read from sysfs.
//
//   - exact: the device itself and every device it is stacked on through slaves/ (multipath paths, device-mapper
//     targets, the "-real" device under a logical volume), recursively;
//   - parents: the whole disks that partitions in that chain are part of (and what those are stacked on);
//   - files: the backing files of loop devices in the chain. A loop device is backed by a file, not a slave.
type candidateIPCBlockAncestry struct {
	exact, parents map[string]struct{}
	files          []string
}

// candidateIPCBlockOverlap reports whether two devices can hold the same data: they share a device in their
// chains, or one is the whole disk of a partition of the other. Two partitions of one disk share only that disk
// as a parent and do not overlap. Devices stacked on the same physical device (two logical volumes on one
// disk) do share it, and are treated as overlapping, since the mapping that would tell them apart is not read.
func candidateIPCBlockOverlap(a, b candidateIPCBlockAncestry) bool {
	meets := func(x, y map[string]struct{}) bool {
		for k := range x {
			if _, ok := y[k]; ok {
				return true
			}
		}
		return false
	}
	return meets(a.exact, b.exact) || meets(a.exact, b.parents) || meets(a.parents, b.exact)
}

// candidateIPCBlockAncestryOf reads the ancestry of the device major:minor. A device sysfs does not list, one
// whose slaves cannot be followed, a chain deeper than 16 or wider than 256 devices, and a file that cannot be
// read are errors: the ancestry would be incomplete.
func candidateIPCBlockAncestryOf(majmin string) (candidateIPCBlockAncestry, error) {
	result := candidateIPCBlockAncestry{exact: map[string]struct{}{}, parents: map[string]struct{}{}}
	if !candidateIPCMajMin.MatchString(majmin) {
		return result, fmt.Errorf("malformed device number %q", majmin)
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(candidateIPCSysDevBlock, majmin))
	if err != nil {
		return result, fmt.Errorf("sysfs has no block device %s: %w", majmin, err)
	}
	if err := result.visit(dir, result.exact, 0); err != nil {
		return result, err
	}
	return result, nil
}

func (r *candidateIPCBlockAncestry) visit(dir string, into map[string]struct{}, depth int) error {
	if depth > candidateIPCBlockDepth {
		return errors.New("the block device chain is too deep")
	}
	if name := filepath.Base(dir); !candidateIPCBlockKinds.MatchString(name) {
		return fmt.Errorf("%s is not a block device kind whose backing sysfs describes", name)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "dev"))
	if err != nil {
		return err
	}
	dev := strings.TrimSpace(string(raw))
	if !candidateIPCMajMin.MatchString(dev) {
		return fmt.Errorf("%s/dev holds %q", dir, dev)
	}
	if _, seen := into[dev]; seen {
		return nil
	}
	if len(r.exact)+len(r.parents) >= candidateIPCBlockNodes {
		return errors.New("the block device chain is too wide")
	}
	into[dev] = struct{}{}
	if raw, err := os.ReadFile(filepath.Join(dir, "loop", "backing_file")); err == nil {
		file := strings.TrimSpace(string(raw))
		switch {
		case strings.HasSuffix(file, " (deleted)"):
			return fmt.Errorf("loop backing file %q is deleted and cannot be judged", file)
		case !filepath.IsAbs(file):
			return fmt.Errorf("loop backing file %q is not absolute", file)
		}
		r.files = append(r.files, file)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "partition")); err == nil {
		// A partition is part of its whole disk, which sits in the directory above it.
		if err := r.visit(filepath.Dir(dir), r.parents, depth+1); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	slaves, err := os.ReadDir(filepath.Join(dir, "slaves"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, slave := range slaves {
		target, err := filepath.EvalSymlinks(filepath.Join(dir, "slaves", slave.Name()))
		if err != nil {
			return fmt.Errorf("slave %s of %s: %w", slave.Name(), dir, err)
		}
		if err := r.visit(target, into, depth+1); err != nil {
			return err
		}
	}
	return nil
}
