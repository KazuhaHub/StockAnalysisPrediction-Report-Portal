// Package cpuset parses and formats Linux CPU lists, the "0-3,5" notation of
// /sys/devices/system/cpu/online, cpuset.cpus.effective and Docker's CpusetCpus.
package cpuset

import (
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
)

// ErrList is returned for text that is not a strictly ascending CPU list.
var ErrList = errors.New("invalid CPU list")

const maxCPU = 1 << 16

// Parse reads a CPU list: comma separated numbers and ascending ranges. The list must be strictly ascending
// with no overlap, so it names every CPU exactly once. One trailing newline is accepted, as in sysfs files.
func Parse(text string) ([]int, error) {
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil, ErrList
	}
	var out []int
	for _, part := range strings.Split(text, ",") {
		lo, hi, isRange := strings.Cut(part, "-")
		first, ok := number(lo)
		if !ok {
			return nil, ErrList
		}
		last := first
		if isRange {
			if last, ok = number(hi); !ok || last <= first {
				return nil, ErrList
			}
		}
		if len(out) > 0 && first <= out[len(out)-1] {
			return nil, ErrList
		}
		for cpu := first; cpu <= last; cpu++ {
			out = append(out, cpu)
		}
	}
	return out, nil
}

func number(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n >= maxCPU || strconv.Itoa(n) != s {
		return 0, false
	}
	return n, true
}

// Format renders strictly ascending CPUs as the minimal range list the kernel itself prints. The empty set is "".
func Format(cpus []int) string {
	var b strings.Builder
	for i := 0; i < len(cpus); {
		j := i
		for j+1 < len(cpus) && cpus[j+1] == cpus[j]+1 {
			j++
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(cpus[i]))
		if j > i {
			b.WriteByte('-')
			b.WriteString(strconv.Itoa(cpus[j]))
		}
		i = j + 1
	}
	return b.String()
}

// Valid reports whether text is a non-empty list in exactly the form Format writes. The runner only ever sends
// this form to Docker, so anything else is a plan it did not build.
func Valid(text string) bool {
	cpus, err := Parse(text)
	return err == nil && Format(cpus) == text
}

// Equal reports whether two CPU sets hold the same members.
func Equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// maxFileBytes bounds a CPU list file; the longest realistic list is a few hundred bytes.
const maxFileBytes = 4096

// ReadFile reads a CPU list from a sysfs or cgroup file. An unreadable, oversized or malformed file is an error,
// never an empty set.
func ReadFile(path string) ([]int, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil || len(raw) > maxFileBytes {
		return nil, ErrList
	}
	return Parse(string(raw))
}
