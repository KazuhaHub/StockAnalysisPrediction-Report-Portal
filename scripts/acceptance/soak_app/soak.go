package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	cgroupRoot       = "/sys/fs/cgroup/"
	dataDirectory    = "/data"
	statusInterval   = 30 * time.Second
	logBytesPerTick  = 100 << 10 // ten ticks a second: one MiB a second
	logTick          = 100 * time.Millisecond
	storageHold      = 2 * time.Minute
	storageChunkSize = 1 << 20
)

type soakState struct {
	served       *atomic.Int64
	heldBytes    atomic.Int64
	threads      atomic.Int64
	burners      atomic.Int64
	logBytes     atomic.Int64
	storageBytes atomic.Int64
	storageFull  atomic.Int64
	dataPresent  atomic.Bool
	spin         atomic.Uint64
}

func newSoakState(served *atomic.Int64) *soakState { return &soakState{served: served} }

func readCgroup(name string) string {
	raw, err := os.ReadFile(cgroupRoot + name)
	if err != nil {
		return ""
	}
	return string(raw)
}

// run starts every load and then reports a status line on the log at a fixed interval. The report is part of
// the log flood, and the only output the soak needs from inside the container.
func (s *soakState) run() {
	limit, known := parseCgroupLimit(readCgroup("memory.max"))
	if !loadsEnabled(limit, known) {
		select {} // a probe image: serve, and do nothing else
	}
	if soft := softMemoryLimit(limit, known); soft > 0 {
		debug.SetMemoryLimit(soft)
	}
	go s.holdMemory(memoryHold(limit, known))
	workers := cpuWorkers(readCgroup("cpu.max"))
	for range workers {
		s.burners.Add(1)
		go s.burnCPU()
	}
	go s.forkThreads()
	go s.floodLog()
	go s.floodStorage()
	for range time.Tick(statusInterval) {
		os.Stderr.Write(append(s.statusJSON(), '\n'))
	}
}

// statusJSON is what the loads have done so far. The Portal reads it through a signed request (the log it is
// also written to is flooded and rotates in seconds, so it is no channel).
func (s *soakState) statusJSON() []byte {
	line, _ := json.Marshal(map[string]any{
		"soak": "status", "held_bytes": s.heldBytes.Load(), "threads": s.threads.Load(),
		"burners": s.burners.Load(), "log_bytes": s.logBytes.Load(),
		"storage_bytes": s.storageBytes.Load(), "storage_full": s.storageFull.Load(),
		"data_present": s.dataPresent.Load(), "requests": s.served.Load(), "goroutines": runtime.NumGoroutine(),
	})
	return line
}

// holdMemory keeps a fixed amount resident: every page is written, and written again each interval, so the
// kernel sees it as in use rather than as untouched virtual memory.
func (s *soakState) holdMemory(total int64) {
	const chunk = 1 << 20
	var held [][]byte
	for allocated := int64(0); allocated+chunk <= total; allocated += chunk {
		block := make([]byte, chunk)
		for i := 0; i < chunk; i += 4096 {
			block[i] = 1
		}
		held = append(held, block)
		s.heldBytes.Add(chunk)
	}
	for range time.Tick(10 * time.Second) {
		for _, block := range held {
			for i := 0; i < len(block); i += 4096 {
				block[i]++
			}
		}
	}
}

// burnCPU is a busy loop. The container's CPU quota, not this code, decides how much of it runs. It runs at the
// lowest priority of its own thread, so that inside the quota the request handlers and the registration handshake
// still win the scheduler: a backend that starved its own readiness would fail its install, not exercise its limit.
func (s *soakState) burnCPU() {
	runtime.LockOSThread()
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, 0, 19)
	x := uint64(88172645463325252)
	for {
		for range 1 << 20 {
			x ^= x << 13
			x ^= x >> 7
			x ^= x << 17
		}
		s.spin.Store(x)
	}
}

// forkThreads parks one OS thread per goroutine until the container is a margin short of its PID limit.
func (s *soakState) forkThreads() {
	limit, known := parseCgroupLimit(readCgroup("pids.max"))
	for {
		current, _ := parseCgroupLimit(readCgroup("pids.current"))
		if threadsToSpawn(limit, current, known) < 1 {
			return
		}
		started := make(chan struct{})
		go func() {
			runtime.LockOSThread()
			close(started)
			select {}
		}()
		<-started
		s.threads.Add(1)
		time.Sleep(20 * time.Millisecond)
	}
}

// floodLog writes lines of about 64 KiB to stderr, the stream the container's log driver keeps.
func (s *soakState) floodLog() {
	line := append(bytes.Repeat([]byte{'l'}, (64<<10)-1), '\n')
	for range time.Tick(logTick) {
		for written := 0; written < logBytesPerTick; written += len(line) {
			n, _ := os.Stderr.Write(line)
			s.logBytes.Add(int64(n))
		}
	}
}

// floodStorage fills the data namespace until the write fails with ENOSPC (the project quota), holds the full
// namespace for a while, then removes the file and fills it again. A namespace that is absent is not an error:
// a backend started without data has nothing to flood.
func (s *soakState) floodStorage() {
	if info, err := os.Stat(dataDirectory); err != nil || !info.IsDir() {
		return
	}
	s.dataPresent.Store(true)
	chunk := bytes.Repeat([]byte{'s'}, storageChunkSize)
	path := dataDirectory + "/soak-fill-" + strconv.Itoa(os.Getpid())
	for {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			time.Sleep(time.Minute)
			continue
		}
		var written int64
		for {
			n, err := file.Write(chunk)
			written += int64(n)
			if err == nil {
				// Dirty page cache is charged to the container's memory. Writing a quarter gigabyte faster than it
				// can be written back would get the backend OOM-killed before it ever reached its quota, so every
				// few MiB are forced to disk.
				if written%(8<<20) == 0 {
					_ = file.Sync()
				}
				continue
			}
			if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
				s.storageFull.Add(1)
			}
			break
		}
		s.storageBytes.Store(written)
		file.Close()
		time.Sleep(storageHold)
		os.Remove(path)
	}
}
