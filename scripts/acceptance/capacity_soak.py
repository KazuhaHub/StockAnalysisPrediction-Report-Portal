#!/usr/bin/env python3
"""Declared-capacity soak on a disposable GitHub-hosted worker (doc 171, PR 4b; OPS-08, closing SEC-09).

It builds a newly owned 16 GiB XFS project-quota loop volume, prepares the runner's journal on it, and runs the
Go acceptance TestPlatformCapacitySoak (a real root runner, a Portal child, fifty installations, the excess probes
and the platform floods) while this process samples the host every thirty seconds: memory, CPU, tasks, the Docker
and runner logs, the XFS volume and the summed memory and CPU of the soak backends. It then judges the Go
evidence and the samples together against the pass criteria of doc 167 section 4 and writes everything as JSON.

Only a run of at least 120 minutes that passes counts as OPS-08 evidence; a shorter run is a smoke of the same
harness. The run is a workflow_dispatch of this branch at the reviewed commit; the evidence records both. Only synthetic fixtures live on the volume, and only the loop device and mount this process created are
ever detached or removed.
"""
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import threading
import time

PATH = "/usr/sbin:/usr/bin:/sbin:/bin"
ENV = {"PATH": PATH, "LC_ALL": "C"}
EVIDENCE_FORMAT = "portal.capacity-soak/1-dev.1"
VOLUME_BYTES = 16 * 1024 * 1024 * 1024
RATIO_LIMIT_NUMERATOR, RATIO_LIMIT_DENOMINATOR = 3, 2  # 1.5x
OPS08_MINUTES = 120
# The step timeout is 160 minutes and the run also builds, installs and judges: a longer soak would be killed, not judged.
MAX_DURATION_MINUTES = 140
SAMPLE_INTERVAL_SECONDS = 30
# At least this share of the expected thirty-second host samples must exist, and at most the rest may have failed.
HOST_SAMPLE_COVERAGE = 0.9
HOST_MIN_AVAILABLE_FRACTION = 0.10
# The message flood is driven at the Portal's admission cap: a handful of requests are answered, the rest are refused 429
# app_platform_busy. Every one-minute window must show answered requests, a typed refusal for the rest, and a live flood.
FLOOD_MIN_ANSWERED_PER_WINDOW = 20
FLOOD_HEARTBEAT_MAX_AGE_MS = 10_000
TYPED_FLOOD_CLASSES = ("200", "429:app_platform_busy")
# What each backend must show in every one-minute window, from its own cgroup counters.
MEMORY_HELD_PERCENT = 85
MEMORY_HELD_FRACTION = MEMORY_HELD_PERCENT / 100.0
CPU_QUOTA_USED_FRACTION = 0.70
PIDS_REACHED_FRACTION = 0.85
# The image store probe needs two images of about 4 GiB each in the daemon's store, plus room for the rest of the run.
IMAGE_STORE_HEADROOM_BYTES = 6 * 1024 ** 3
IMAGE_STORE_NATIVE_BYTES = 8 * 1024 ** 3 + IMAGE_STORE_HEADROOM_BYTES
MEBIBYTE = 1024 ** 2
# The runner's own default of capacity.cpu_percent (internal/apprunner/service/capacity.go). A run that overrides it measures a
# different budget and is evidence only if it names this value.
RUNNER_DEFAULT_CPU_PERCENT = 50
# The runner's default of capacity.reserved_cpus on a host of four or more CPUs (internal/apprunner/service/capacity.go).
RUNNER_DEFAULT_RESERVED_CPUS = 1
RETIRED_PROBES = ("ninth_backend", "memory_over_budget", "cpu_over_budget")
REQUIRED_PROBES = (
    "fifty_first_installation",
    "ninth_backend",
    "memory_over_budget",
    "cpu_over_budget",
    "image_beyond_the_image_store",
    "layer_beyond_its_declaration",
    "staging_over_two_gib",
    "task_over_installation_quota",
    "task_over_global_quota",
    "event_subscription_over_quota",
)


# ---- parsing ----

def parse_meminfo(text):
    values = {}
    for line in text.splitlines():
        match = re.match(r"^(MemTotal|MemAvailable):\s+([0-9]+) kB$", line)
        if match:
            values[match.group(1)] = int(match.group(2))
    if set(values) != {"MemTotal", "MemAvailable"}:
        raise ValueError("meminfo lacks MemTotal or MemAvailable")
    return {"total_kib": values["MemTotal"], "available_kib": values["MemAvailable"]}


def parse_proc_stat(text):
    """(busy, total) jiffies of the aggregate cpu line."""
    first = text.splitlines()[0].split()
    if first[0] != "cpu" or len(first) < 9:
        raise ValueError("unexpected /proc/stat")
    user, nice, system, idle, iowait, irq, softirq, steal = (int(v) for v in first[1:9])
    busy = user + nice + system + irq + softirq + steal
    return busy, busy + idle + iowait


def cpu_percent(previous, current):
    busy, total = current[0] - previous[0], current[1] - previous[1]
    return 100.0 * busy / total if total > 0 else 0.0


def parse_loadavg_tasks(text):
    """(runnable, total) kernel scheduling entities, the host's task count."""
    match = re.match(r"^\S+ \S+ \S+ ([0-9]+)/([0-9]+) [0-9]+\s*$", text)
    if not match:
        raise ValueError("unexpected /proc/loadavg")
    return int(match.group(1)), int(match.group(2))


DOCKER_UNITS = {"B": 1, "kB": 1000, "KB": 1000, "MB": 1000 ** 2, "GB": 1000 ** 3, "TB": 1000 ** 4,
                "KiB": 1024, "MiB": 1024 ** 2, "GiB": 1024 ** 3, "TiB": 1024 ** 4}


def parse_docker_size(text):
    match = re.match(r"^([0-9]+(?:\.[0-9]+)?)\s*([A-Za-z]+)$", text.strip())
    if not match or match.group(2) not in DOCKER_UNITS:
        raise ValueError("unrecognised docker size: " + text)
    return int(float(match.group(1)) * DOCKER_UNITS[match.group(2)])


def parse_docker_stats_row(line):
    row = json.loads(line)
    percent = re.match(r"^([0-9]+(?:\.[0-9]+)?)%$", row["CPUPerc"])
    if not percent:
        raise ValueError("unrecognised CPU percentage: " + row["CPUPerc"])
    return {"id": row["ID"], "memory_bytes": parse_docker_size(row["MemUsage"].split("/")[0]),
            "cpu_millis": float(percent.group(1)) * 10.0, "pids": int(row["PIDs"])}


def parse_cgroup_usage_usec(text):
    for line in text.splitlines():
        if line.startswith("usage_usec "):
            return int(line.split()[1])
    raise ValueError("cpu.stat has no usage_usec")


def parse_cgroup_number(text):
    """A cgroup v2 counter or limit; None for "max" (no limit)."""
    value = text.strip()
    if value == "max":
        return None
    return int(value)


def parse_cpu_max_millis(text):
    """The CPU quota of cgroup v2 cpu.max in thousandths of a core; None when there is no quota."""
    fields = text.split()
    if len(fields) != 2 or fields[0] == "max":
        return None
    return int(fields[0]) / int(fields[1]) * 1000.0


def cpu_millis_between(previous_usec, current_usec, seconds):
    """Average CPU use over an interval, in thousandths of a core: the cgroup's own count, not a rate sample."""
    if seconds <= 0:
        return 0.0
    return (current_usec - previous_usec) / (seconds * 1000.0)


# ---- topology ----

def uses_eevdf(kernel):
    """The kernel's own scheduler is EEVDF from 6.6 (CFS before it)."""
    match = re.match(r"^([0-9]+)\.([0-9]+)", kernel or "")
    return bool(match) and (int(match.group(1)), int(match.group(2))) >= (6, 6)


def read_thread_siblings(cpu_root="/sys/devices/system/cpu"):
    siblings = {}
    for folder in sorted(Path(cpu_root).glob("cpu[0-9]*")):
        try:
            siblings[folder.name] = (folder / "topology" / "thread_siblings_list").read_text().strip()
        except OSError:
            pass
    return siblings


def smt_layout(siblings):
    """Whether any CPU shares a physical core with another, and which CPUs share cpu0's core."""
    return {"thread_siblings": siblings, "smt": any(re.search(r"[,-]", v) for v in siblings.values()), "cpu0_siblings": siblings.get("cpu0")}


def collect_topology(run):
    """uname, lscpu and every CPU's thread siblings, for the evidence."""
    kernel = os.uname().release
    topology = {"kernel": kernel, "eevdf": uses_eevdf(kernel), **smt_layout(read_thread_siblings())}
    try:
        topology["lscpu"] = run("lscpu")
    except (RuntimeError, OSError, subprocess.SubprocessError) as error:
        topology["lscpu"] = "unavailable: " + str(error)
    return topology


def normalise_comm(comm):
    """A command name with its numbers replaced, so kworker/u8:3 and kworker/u8:5 are one consumer."""
    return re.sub(r"[0-9]+", "N", comm)


MODE_NAMES = ("user", "nice", "system", "idle", "iowait", "irq", "softirq", "steal")


def parse_cpu_modes(text):
    f = text.splitlines()[0].split()
    return [int(v) for v in f[1:9]]


def cpu_modes(before, after):
    """The host's CPU time by mode between two readings, in percent: user, system, interrupts, softirq, hypervisor steal, idle."""
    delta = [a - b for a, b in zip(after, before)]
    total = sum(delta)
    return {name: round(100.0 * d / total, 2) for name, d in zip(MODE_NAMES, delta)} if total > 0 else {}


# ---- which CPUs ----

def parse_per_cpu(text):
    """{"cpu0": (busy, total), ...} jiffies from the per-cpu lines of /proc/stat."""
    cores = {}
    for line in text.splitlines():
        f = line.split()
        if f and re.fullmatch(r"cpu[0-9]+", f[0]) and len(f) >= 9:
            user, nice, system, idle, iowait, irq, softirq, steal = (int(v) for v in f[1:9])
            busy = user + nice + system + irq + softirq + steal
            cores[f[0]] = (busy, busy + idle + iowait)
    return cores


def per_cpu_busy(before, after):
    return {name: round(100.0 * (b - before[name][0]) / (t - before[name][1]), 2) for name, (b, t) in sorted(after.items())
            if name in before and t > before[name][1]}


def parse_ready_cpus(text):
    """reserved_cpus and app_cpus of the runner's ready line."""
    match = re.search(r"app-runner ready .*?reserved_cpus=(\S+) app_cpus=(\S+)", text)
    return {"reserved_cpus": match.group(1), "app_cpus": match.group(2)} if match else {}


# ---- where the time goes: stalls, disks, PostgreSQL ----

def parse_pressure(text):
    """/proc/pressure/* or a cgroup's *.pressure: {"some": {"avg10", "total_us"}, "full": ...}."""
    result = {}
    for line in text.splitlines():
        match = re.match(r"^(some|full) avg10=([0-9.]+) avg60=[0-9.]+ avg300=[0-9.]+ total=([0-9]+)$", line.strip())
        if match:
            result[match.group(1)] = {"avg10": float(match.group(2)), "total_us": int(match.group(3))}
    if not result:
        raise ValueError("no pressure lines")
    return result


def read_group_pressure(cgroup_fs, path):
    """cpu, io and memory pressure of one cgroup; empty when its files are not there."""
    result = {}
    for resource in ("cpu", "io", "memory"):
        try:
            result[resource] = parse_pressure((Path(cgroup_fs) / path.lstrip("/") / f"{resource}.pressure").read_text())
        except (OSError, ValueError):
            pass
    return result


def parse_diskstats(text):
    """/proc/diskstats keyed by "major:minor"."""
    stats = {}
    for line in text.splitlines():
        f = line.split()
        if len(f) >= 14:
            stats[f"{f[0]}:{f[1]}"] = {"name": f[2], "reads": int(f[3]), "rd_sectors": int(f[5]), "rd_ms": int(f[6]), "writes": int(f[7]),
                                       "wr_sectors": int(f[9]), "wr_ms": int(f[10]), "io_ticks_ms": int(f[12])}
    return stats


def disk_delta(before, after, seconds):
    """Utilisation (share of the interval the device had I/O in flight), mean await, IOPS and write rate between two readings."""
    ios = (after["reads"] - before["reads"]) + (after["writes"] - before["writes"])
    wait_ms = (after["rd_ms"] - before["rd_ms"]) + (after["wr_ms"] - before["wr_ms"])
    return {"utilisation_percent": round(100.0 * (after["io_ticks_ms"] - before["io_ticks_ms"]) / (seconds * 1000.0), 2),
            "await_ms": round(wait_ms / ios, 3) if ios else 0.0, "iops": round(ios / seconds, 2),
            "write_kib_per_second": round((after["wr_sectors"] - before["wr_sectors"]) * 512 / 1024.0 / seconds, 1)}


def storage_layout(devices, stats, extra):
    """Which device each volume lives on, and whether PostgreSQL's data and the XFS loop volume's backing file share a disk."""
    layout = {name: {"major_minor": dev, "device": (stats.get(dev) or {}).get("name")} for name, dev in devices.items()}
    layout["postgres_and_xfs_backing_share_a_disk"] = devices.get("postgres_data") is not None and devices.get("postgres_data") == devices.get("xfs_backing_file")
    layout.update(extra)
    return layout


PG_WAL_QUERY = "SELECT (SELECT sum(xact_commit) FROM pg_stat_database) || '|' || wal_sync || '|' || wal_sync_time FROM pg_stat_wal"


def parse_pg_wal(text):
    commits, syncs, sync_ms = text.strip().split("|")
    return {"commits": float(commits), "syncs": float(syncs), "sync_ms": float(sync_ms)}


def pg_delta(before, after, seconds):
    syncs = after["syncs"] - before["syncs"]
    return {"commits_per_second": round((after["commits"] - before["commits"]) / seconds, 2), "wal_syncs_per_second": round(syncs / seconds, 2),
            "mean_wal_sync_ms": round((after["sync_ms"] - before["sync_ms"]) / syncs, 3) if syncs else 0.0}


def _meminfo_kib(text, key):
    match = re.search(rf"^{key}:\s+([0-9]+) kB$", text, re.MULTILINE)
    return int(match.group(1)) if match else 0


def _stat_counter(text, key):
    match = re.search(rf"^{key} ([0-9]+)$", text, re.MULTILINE)
    return int(match.group(1)) if match else 0


# ---- who uses the CPU ----

CPU_GROUPS = ("portal", "flood", "postgres", "runner", "docker", "backends", "other")


def classify_process(comm, cmdline):
    """The group of a host process: the measured Portal, the flood generator, the app runner (the three test processes of the
    soak binary, told apart by their -test.run), PostgreSQL, Docker's daemons, or everything else. The eight backends are not
    classified here: they are read from their cgroups."""
    args = " ".join(cmdline)
    if "-test.run=^TestPlatformCapacitySoakChild$" in args:
        return "portal"
    if "-test.run=^TestPlatformCapacitySoakFlood$" in args:
        return "flood"
    if "-test.run=^TestPlatformRunnerServiceChild$" in args:
        return "runner"
    if comm in ("postgres", "postmaster") or (cmdline and cmdline[0].startswith("/usr/lib/postgresql")):
        return "postgres"
    if comm in ("dockerd", "containerd", "runc") or comm.startswith("containerd-shim"):
        return "docker"
    return "other"


def _read_cgroup_path(proc, pid):
    for line in (Path(proc) / str(pid) / "cgroup").read_text().splitlines():
        if line.startswith("0::"):
            return line[3:]
    return None


def _weights_along(cgroup_fs, path):
    """cpu.weight of every cgroup on the path, from the top slice down (the root cgroup has none)."""
    weights, current = {}, ""
    for part in [p for p in path.split("/") if p]:
        current += "/" + part
        try:
            weights[current] = (Path(cgroup_fs) / current.lstrip("/") / "cpu.weight").read_text().strip()
        except OSError:
            weights[current] = None
    return weights


def cgroup_layout(proc, cgroup_fs, backend_full_ids, docker_slice):
    """Where each process group sits in the cgroup tree and the cpu.weight along its path: the measured Portal, the flood, the runner,
    PostgreSQL and one backend container. A weight arbitrates only between sibling cgroups, so this is what says whether a weight set on
    the app containers can reach the processes it is meant to yield to. A group that is not running is absent, never invented."""
    layout = {}
    for folder in sorted((p for p in Path(proc).iterdir() if p.name.isdigit()), key=lambda p: int(p.name)):
        try:
            stat = (folder / "stat").read_text()
            comm = stat[stat.index("(") + 1:stat.rindex(")")]
            cmdline = (folder / "cmdline").read_bytes().decode("utf-8", "replace").split("\0")
            group = classify_process(comm, cmdline)
            path = _read_cgroup_path(proc, folder.name)
        except (OSError, ValueError):
            continue
        if group in ("portal", "flood", "runner", "postgres") and group not in layout and path:
            layout[group] = {"pid": int(folder.name), "cgroup": path, "cpu_weights": _weights_along(cgroup_fs, path)}
            try:  # the affinity the process really has: a scope's AllowedCPUs and the cpuset above it show up here
                for line in (folder / "status").read_text().splitlines():
                    if line.startswith("Cpus_allowed_list:"):
                        layout[group]["cpus_allowed"] = line.split(":", 1)[1].strip()
            except OSError:
                pass
    for full_id in sorted(backend_full_ids)[:1]:
        path = f"{docker_slice.rstrip('/')}/docker-{full_id}.scope"
        layout["backend"] = {"cgroup": path, "cpu_weights": _weights_along(cgroup_fs, path)}
    return layout


def device_number(path):
    """"major:minor" of the device a path lives on."""
    st = os.stat(path)
    return f"{os.major(st.st_dev)}:{os.minor(st.st_dev)}"


def require_systemd_scopes(run):
    """The soak mirrors production by starting the Portal and the runner in their own scopes under system.slice. If systemd-run cannot do that
    here the run stops: a layout is never faked."""
    try:
        run("systemd-run", "--scope", "--quiet", "--collect", "--slice=system.slice", "--unit=soak-probe-" + str(os.getpid()), "-p", "CPUWeight=100", "--", "true")
    except (RuntimeError, OSError, subprocess.SubprocessError) as error:
        raise RuntimeError("systemd-run cannot start a transient scope under system.slice on this host; the soak will not fake the production cgroup layout: " + str(error)) from error


# ---- sampling ----

class Sampler:
    """One host sample: memory, CPU, tasks, volume, container logs, and the sum over the soak backends.

    `run` runs a command and returns its text (injected so the parsing is tested without a Docker daemon)."""

    def __init__(self, run, proc, mountpoint, image_id, clock=time.time, cgroup_root="/sys/fs/cgroup/system.slice", cpus=None, ticks_per_second=None,
                 disk_devices=None, postgres=False):
        self.disk_devices, self.postgres = disk_devices or {}, postgres
        self._disks, self._pg, self._iowait, self._cores = None, None, None, None
        self._pid_comm = {}
        self._modes = None
        self.cpus = cpus or os.cpu_count() or 1
        self.ticks_per_second = ticks_per_second or os.sysconf("SC_CLK_TCK")
        self._backend_full_ids = set()
        self._process_ticks = {}
        self._previous_at = None
        self.layout = {}
        self.run, self.proc, self.mountpoint, self.image_id, self.clock = run, Path(proc), Path(mountpoint), image_id, clock
        self.cgroup_root = Path(cgroup_root)
        self.cgroup_fs = Path("/sys/fs/cgroup")
        self._cpu = None
        self._cgroup = {}

    def _backends(self, now):
        ids = self.run("docker", "ps", "-a", "--filter", "ancestor=" + self.image_id, "--format", "{{.ID}}").split()
        summary = {"total": len(ids), "running": 0, "restarting": 0, "exited": 0, "oom_killed": 0, "restart_count": 0,
                   "memory_bytes": 0, "cpu_millis": 0.0, "pids": 0, "cpu_source": "docker_stats"}
        log_bytes, running, full_ids = 0, [], {}
        for container in ids:
            detail = json.loads(self.run("docker", "inspect", "--format", "{{json .}}", container))
            state = detail["State"]
            if state.get("Running"):
                summary["running"] += 1
                running.append(container)
            elif state.get("Restarting"):
                summary["restarting"] += 1
            else:
                summary["exited"] += 1
            summary["oom_killed"] += 1 if state.get("OOMKilled") else 0
            summary["restart_count"] += int(detail.get("RestartCount", 0))
            if detail.get("Id"):
                full_ids[container] = detail["Id"]
            path = detail.get("LogPath")
            for candidate in ([path] + [f"{path}.{n}" for n in (1, 2, 3)]) if path else []:
                try:
                    log_bytes += os.path.getsize(candidate)
                except OSError:
                    pass
        self._backend_full_ids = set(full_ids.values())
        if running:
            rows = [parse_docker_stats_row(line) for line in
                    self.run("docker", "stats", "--no-stream", "--format", "{{json .}}", *running).splitlines() if line.strip()]
            summary["memory_bytes"] = sum(r["memory_bytes"] for r in rows)
            summary["cpu_millis"] = sum(r["cpu_millis"] for r in rows)
            summary["pids"] = sum(r["pids"] for r in rows)
            counters = self._cgroup_counters(running, full_ids)
            if counters is not None:
                summary["per_backend"] = [dict(id=container, **counters[container]) for container in running]
                cgroup = self._cgroup_cpu_millis(counters, now)
                if cgroup is not None:
                    summary["cpu_millis"], summary["cpu_source"] = cgroup, "cgroup"
        return summary, log_bytes

    def _read_cgroup(self, full_id):
        """One backend's own kernel counters: CPU used, memory and PIDs held against their limits, and the peaks the kernel keeps."""
        base = self.cgroup_root / f"docker-{full_id}.scope"
        row = {"usage_usec": parse_cgroup_usage_usec((base / "cpu.stat").read_text()),
               "memory_current": parse_cgroup_number((base / "memory.current").read_text()),
               "memory_max": parse_cgroup_number((base / "memory.max").read_text()),
               "cpu_quota_millis": parse_cpu_max_millis((base / "cpu.max").read_text()),
               "pids_current": parse_cgroup_number((base / "pids.current").read_text()),
               "pids_max": parse_cgroup_number((base / "pids.max").read_text())}
        try:
            row["cpuset_effective"] = (base / "cpuset.cpus.effective").read_text().strip()
        except OSError:
            pass
        for name, key in (("memory.peak", "memory_peak"), ("pids.peak", "pids_peak")):
            try:  # the peak files exist from kernel 5.19 and 6.1; the sampled current values stand in where they do not
                row[key] = parse_cgroup_number((base / name).read_text())
            except (OSError, ValueError):
                pass
        return row

    def _cgroup_counters(self, running, full_ids):
        counters = {}
        for container in running:
            if container not in full_ids:
                return None
            try:
                counters[container] = self._read_cgroup(full_ids[container])
            except (OSError, ValueError):
                return None
        return counters

    def _cgroup_cpu_millis(self, counters, now):
        """The summed average CPU of the running backends since the previous sample, from the kernel's counters.

        This is the figure the budget is judged on when it is available: a rate sampled over a second can read a
        few percent above a quota that the kernel enforces exactly over any longer window."""
        current = {container: (row["usage_usec"], now) for container, row in counters.items()}
        previous, self._cgroup = self._cgroup, current
        if set(previous) != set(current) or not previous:
            return None
        return sum(cpu_millis_between(previous[c][0], current[c][0], current[c][1] - previous[c][1]) for c in current)

    def _group_ticks(self):
        """CPU ticks (utime + stime) by group since each process started, for every process outside a backend container.
        A process that exits between samples is not counted here; the rest of the host's busy time lands in "other"."""
        groups = {}
        for folder in self.proc.iterdir():
            if not folder.name.isdigit():
                continue
            try:
                stat = (folder / "stat").read_text()
                fields = stat[stat.rindex(")") + 2:].split()
                ticks = int(fields[11]) + int(fields[12])
                comm = stat[stat.index("(") + 1:stat.rindex(")")]
                cgroup = (folder / "cgroup").read_text()
                if any(full_id in cgroup for full_id in self._backend_full_ids):
                    continue  # inside a backend container: counted from the container's own cgroup
                cmdline = (folder / "cmdline").read_bytes().decode("utf-8", "replace").split("\0")
            except (OSError, ValueError, IndexError):
                continue
            group = classify_process(comm, cmdline)
            groups.setdefault(group, {})[int(folder.name)] = ticks
            self._pid_comm[int(folder.name)] = normalise_comm(comm)
        return groups

    def _cpu_groups(self, now, host_percent, backend_millis):
        """Each group's share of the host's CPU between this sample and the previous one, in percent of all CPUs."""
        groups = self._group_ticks()
        flat = {pid: ticks for members in groups.values() for pid, ticks in members.items()}
        previous, self._process_ticks = self._process_ticks, flat
        seconds = None if self._previous_at is None else now - self._previous_at
        self._previous_at = now
        if not previous or not seconds or seconds <= 0 or host_percent is None:
            return None
        capacity = seconds * self.ticks_per_second * self.cpus
        shares = {name: 100.0 * sum(max(t - previous.get(pid, 0), 0) for pid, t in members.items()) / capacity
                  for name, members in groups.items() if name != "other"}
        for name in ("portal", "flood", "postgres", "runner", "docker"):
            shares.setdefault(name, 0.0)
        detail = {}
        for name in ("docker", "other"):
            for pid, ticks in groups.get(name, {}).items():
                share = 100.0 * max(ticks - previous.get(pid, 0), 0) / capacity
                key = f"{name}.{self._pid_comm.get(pid, '?')}"
                detail[key] = detail.get(key, 0.0) + share
        self.last_detail = detail
        shares["backends"] = backend_millis / (self.cpus * 10.0)
        shares["host_busy"] = host_percent
        shares["other"] = max(0.0, host_percent - sum(v for k, v in shares.items() if k != "host_busy"))
        return shares

    def sample(self):
        now = self.clock()
        memory_text, stat_text = (self.proc / "meminfo").read_text(), (self.proc / "stat").read_text()
        memory = parse_meminfo(memory_text)
        cpu = parse_proc_stat(stat_text)
        percent = None if self._cpu is None else cpu_percent(self._cpu, cpu)
        self._cpu = cpu
        running_tasks, total_tasks = parse_loadavg_tasks((self.proc / "loadavg").read_text())
        volume = os.statvfs(self.mountpoint)
        backends, log_bytes = self._backends(now)
        try:
            fresh = cgroup_layout(self.proc, self.cgroup_fs, self._backend_full_ids, str(self.cgroup_root).replace("/sys/fs/cgroup", "") or "/")
            if len(fresh) >= len(self.layout):
                self.layout = fresh  # the fullest picture seen: the flood and the backends exist only once the load runs
        except OSError:
            pass
        extra = self._stalls(now, memory_text, stat_text)
        modes = parse_cpu_modes(stat_text)
        if self._modes is not None:
            extra["cpu_modes"] = cpu_modes(self._modes, modes)
        self._modes = modes
        cores = parse_per_cpu(stat_text)
        previous_cores, self._cores = self._cores, cores
        if previous_cores:
            extra["cpu_per_core"] = per_cpu_busy(previous_cores, cores)
        shares = self._cpu_groups(now, percent, backends["cpu_millis"] if backends.get("cpu_source") == "cgroup" else 0.0)
        row = {
            "at_ms": int(now * 1000), "mem_total_kib": memory["total_kib"], "mem_available_kib": memory["available_kib"],
            "host_cpu_percent": percent, "tasks_running": running_tasks, "tasks_total": total_tasks,
            "xfs_total_bytes": volume.f_blocks * volume.f_frsize,
            "xfs_used_bytes": (volume.f_blocks - volume.f_bfree) * volume.f_frsize,
            "docker_log_bytes": log_bytes, "backends": backends,
        }
        if shares is not None:
            row["cpu_groups"] = shares
            row["cpu_detail"] = {k: round(v, 3) for k, v in self.last_detail.items() if v > 0}
        row.update(extra)
        return row

    def _stalls(self, now, memory_text, stat_text):
        """Pressure-stall, I/O-wait, dirty-page, disk and PostgreSQL WAL sync readings of this sample."""
        extra = {"vm": {"procs_running": _stat_counter(stat_text, "procs_running"), "procs_blocked": _stat_counter(stat_text, "procs_blocked"),
                        "dirty_kib": _meminfo_kib(memory_text, "Dirty"), "writeback_kib": _meminfo_kib(memory_text, "Writeback")}}
        pressure = {}
        host = {}
        for resource in ("cpu", "io", "memory"):
            try:
                host[resource] = parse_pressure((self.proc / "pressure" / resource).read_text())
            except (OSError, ValueError):
                pass
        if host:
            pressure["host"] = host
        for group in ("portal", "postgres", "backend"):
            path = (self.layout.get(group) or {}).get("cgroup")
            found = read_group_pressure(self.cgroup_fs, path) if path else {}
            if found:
                pressure[group] = found
        if pressure:
            extra["pressure"] = pressure
        iowait_line = [l for l in stat_text.splitlines() if l.startswith("cpu ")]
        if iowait_line:
            iowait = int(iowait_line[0].split()[5])
            total = parse_proc_stat(stat_text)[1]
            if self._iowait is not None and total > self._iowait[1]:
                extra["vm"]["iowait_percent"] = round(100.0 * (iowait - self._iowait[0]) / (total - self._iowait[1]), 2)
            self._iowait = (iowait, total)
        if self.disk_devices:
            try:
                stats = parse_diskstats((self.proc / "diskstats").read_text())
            except OSError:
                stats = {}
            previous, self._disks = self._disks, (now, stats)
            if previous and now > previous[0]:
                extra["disk"] = {name: disk_delta(previous[1][dev], stats[dev], now - previous[0])
                                 for name, dev in self.disk_devices.items() if dev in stats and dev in previous[1]}
        if self.postgres:
            try:
                reading = parse_pg_wal(self.run("runuser", "-u", "postgres", "--", "psql", "-tAXc", PG_WAL_QUERY))
            except Exception:  # noqa: BLE001 - a failed query is a missing reading, not a failed sample
                reading = None
            if reading is not None:
                previous, self._pg = self._pg, (now, reading)
                if previous and now > previous[0]:
                    extra["pg"] = pg_delta(previous[1], reading, now - previous[0])
        return extra


# ---- judging ----

def _check(name, passed, detail):
    return {"name": name, "passed": bool(passed), "detail": detail}


# Owner decision 2026-11-15: under full declared load on the reference host, the pooled p95 of home and report is at most 50 ms and of batch
# start at most 100 ms. The relative floor (owner decision 2026-11-13: max(1.5x the idle p95, the idle p95 plus 5 ms)) was measured and could not be
# met on a 2-physical-core host at full app CPU load (doc 171); dispatch@200 keeps it. For the three absolute series the relative threshold and
# the ratio to idle stay recorded as diagnostics, with every window's p95: none is hidden, none decides.
#
# Verdict computation (engineering decision, not the owner's): the p95 of every loaded sample of the run pooled together; per-window values are
# diagnostics.
LATENCY_FLOOR_US = 5000
HOME_P95_BOUND_US = 50_000
REPORT_P95_BOUND_US = 50_000
BATCH_START_P95_BOUND_US = 100_000
ABSOLUTE_P95_BOUND_US = {"home": HOME_P95_BOUND_US, "report": REPORT_P95_BOUND_US, "batch_start": BATCH_START_P95_BOUND_US}
LATENCY_SERIES = ("home", "report", "dispatch", "batch_start")


def latency_threshold_us(idle_p95_us):
    return max(idle_p95_us * RATIO_LIMIT_NUMERATOR // RATIO_LIMIT_DENOMINATOR, idle_p95_us + LATENCY_FLOOR_US)


def _latency_check(name, series, evidence):
    baseline = max(evidence.get("idle", {}).get(series, {}).get("p95_us", 0), 0)
    relative = latency_threshold_us(baseline)
    bound = ABSOLUTE_P95_BOUND_US.get(series, relative)
    pooled = evidence.get("loaded", {}).get(series, {})
    pooled_p95, pooled_count = pooled.get("p95_us", 0), pooled.get("count", 0)
    rows, over, empty, worst = [], [], [], None
    for window in evidence.get("windows", []):
        stats = window.get(series) or {}
        p95 = stats.get("p95_us", 0)
        ok = p95 <= bound
        rows.append({"minute": window["minute"], "p95_us": p95, "threshold_us": bound, "relative_threshold_us": relative, "passed": ok})
        if not ok:
            over.append(window["minute"])
        if not stats.get("count", 0):
            empty.append(window["minute"])
        if worst is None or p95 > worst["p95_us"]:
            worst = {"minute": window["minute"], "p95_us": p95}
    return _check(name, pooled_count > 0 and pooled_p95 <= bound and not empty, {
        "idle_p95_us": baseline, "threshold_us": bound, "bound_us": bound, "bound_kind": "absolute" if series in ABSOLUTE_P95_BOUND_US else "relative",
        "relative_threshold_us": relative, "within_relative_threshold": pooled_count > 0 and pooled_p95 <= relative,
        "pooled_p95_us": pooled_p95, "pooled_samples": pooled_count,
        "pooled_ratio": round(pooled_p95 / max(baseline, 1), 3), "worst_ratio": round((worst or {"p95_us": 0})["p95_us"] / max(baseline, 1), 3),
        "worst_window": worst, "windows": len(rows), "windows_over_count": len(over), "windows_over_the_threshold": over[:200],
        "windows_without_samples": empty[:200], "window_thresholds": rows})


def render_summary(evidence, verdict):
    """Markdown for the job's step summary: the verdict per series and every window of every series."""
    lines = [f"## Capacity soak ({evidence.get('duration_minutes')} minutes): {'passed' if verdict['passed'] else 'FAILED'}",
             "", "Latency verdict: home and report pooled loaded p95 at most 50 ms, batch start at most 100 ms (owner decision 2026-11-15); dispatch at most "
             "max(1.5 x idle p95, idle p95 + 5 ms). The pooling is an engineering decision; windows, the relative threshold and the ratio to idle are diagnostics."]
    for series in LATENCY_SERIES:
        check = next((c for c in verdict["checks"] if c["name"].startswith(series)), None)
        if check is None:
            continue
        d = check["detail"]
        lines += ["", f"### {series}", "",
                  f"idle p95 {d['idle_p95_us']} us, threshold {d['threshold_us']} us, **pooled loaded p95 {d['pooled_p95_us']} us** over "
                  f"{d['pooled_samples']} samples ({'pass' if check['passed'] else 'FAIL'}); {d['windows_over_count']} of {d['windows']} windows over "
                  f"the threshold, worst window {d['worst_window']}", "",
                  "| minute | window p95 (us) | threshold (us) | over |", "| --- | --- | --- | --- |"]
        for row in d["window_thresholds"]:
            lines.append(f"| {row['minute']} | {row['p95_us']} | {row['threshold_us']} | {'' if row['passed'] else 'over'} |")
    breakdown = verdict.get("cpu_breakdown") or {}
    if breakdown.get("windows"):
        names = ("host_busy",) + CPU_GROUPS
        lines += ["", "### CPU by group (percent of all CPUs)", "", "| period | " + " | ".join(names) + " | admitted/s | refused/s |",
                  "| --- |" + " --- |" * (len(names) + 2)]
        rows = [("idle", breakdown.get("idle", {}), None)] + [(f"minute {w['minute']}", w, r) for w, r in zip(breakdown["windows"], breakdown["flood_rates"])]
        for label, shares, rate in rows:
            cells = " | ".join(str(shares.get(n, "")) for n in names)
            lines.append(f"| {label} | {cells} | {rate['admitted_per_second'] if rate else ''} | {rate['refused_per_second'] if rate else ''} |")
    achieved = verdict.get("achieved_fractions") or {}
    if achieved:
        lines += ["", "### Backend fractions achieved (lowest backend)", ""]
        lines += [f"- {name}: {value['min']:.3f} (per backend {', '.join(f'{v:.3f}' for v in value['per_backend'].values())})"
                  for name, value in achieved.items() if value.get("per_backend")]
    for item in verdict.get("unit_level_probes", []):
        lines.append(f"- unit level, not observed in this run: {item['name']} (tests {', '.join(item['tests'])}: "
                     f"{'passed' if item['tests_passed'] else 'NOT PASSED'})")
    return "\n".join(lines) + "\n"


def _window_bounds(evidence):
    """(minute, start_ms, end_ms) of every loaded window: the one-minute interval its samples were spread over."""
    started = evidence.get("load_started_ms", 0)
    bounds = []
    for window in evidence.get("windows", []):
        start = window.get("started_ms") or started + (window["minute"] - 1) * 60_000
        bounds.append((window["minute"], start, window.get("ended_ms") or start + 60_000))
    return bounds


def _loads_check(evidence):
    """Each backend's own status shows its loads ran, and keeps showing it: memory held at the held fraction of its limit,
    CPU burners, threads, log written and the data namespace filled to its quota, with the log and request counters
    advancing in every window. Without this a backend that did nothing, or stopped, would pass."""
    windows = evidence.get("windows", [])
    series = {}
    for window in windows:
        for b in window.get("backends") or []:
            series.setdefault(b.get("installation"), []).append((window["minute"], b))
    held_minimum = (evidence.get("sizing", {}).get("MemoryMiB", 0) * MEMORY_HELD_PERCENT // 100) * MEBIBYTE
    expected = evidence.get("budgets", {}).get("running_backends", 0)
    allowed_missing = max(1, int(0.02 * len(windows)))
    problems = []
    for installation, rows in series.items():
        reasons = []
        answered = [(minute, b) for minute, b in rows if b.get("answered")]
        if len(rows) - len(answered) > allowed_missing or not answered:
            reasons.append(f"did not answer in {len(rows) - len(answered)} of {len(rows)} windows")
        if answered:
            last = answered[-1][1]
            if held_minimum <= 0 or last["held_bytes"] < held_minimum:
                reasons.append(f"held too little memory ({last['held_bytes']} of at least {held_minimum} bytes)")
            if last["threads"] < 8:
                reasons.append("started too few threads")
            if last["burners"] < 1:
                reasons.append("burned no CPU")
            if last["log_bytes"] <= 0:
                reasons.append("wrote no log")
            if not last["data_present"]:
                reasons.append("had no data namespace")
            elif last["storage_full"] < 1:
                reasons.append("never filled its data namespace")
            for (_, before), (minute, after) in zip(answered, answered[1:]):
                for counter in ("log_bytes", "requests"):
                    if after.get(counter, 0) <= before.get(counter, 0):
                        reasons.append(f"{counter} did not advance in window {minute}")
        if reasons:
            problems.append({"installation": installation, "reasons": reasons[:20]})
    return _check("backends_ran_every_declared_load", expected > 0 and len(series) == expected and not problems,
                  {"backends": len(series), "expected": expected, "windows": len(windows), "problems": problems})


def _fractions(values):
    return {"min": min(values.values()), "max": max(values.values()), "per_backend": values} if values else {}


def _backend_limits(evidence, samples):
    """Judge each backend against its own cgroup counters, window by window: CPU used at least CPU_QUOTA_USED_FRACTION of
    its quota, memory current at least MEMORY_HELD_FRACTION of its limit, and PIDs up to the limit less a small margin.
    The status counters the backend reports about itself are checked separately; these cannot be reported by the backend."""
    rows = sorted((s for s in samples if (s.get("backends") or {}).get("per_backend")), key=lambda s: s["at_ms"])
    expected = evidence.get("budgets", {}).get("running_backends", 0)
    problems, cpu_min, memory_min, memory_peak, pids_peak = [], {}, {}, {}, {}
    bounds = _window_bounds(evidence)
    for minute, start, end in bounds:
        in_window = [(i, r) for i, r in enumerate(rows) if start < r["at_ms"] <= end]
        if not in_window:
            problems.append({"window": minute, "backend": None, "reason": "no cgroup sample in the window"})
            continue
        used, available = {}, {}
        for i, current in in_window:
            if i == 0:
                continue
            previous = rows[i - 1]
            seconds = (current["at_ms"] - previous["at_ms"]) / 1000.0
            before = {b["id"]: b for b in previous["backends"]["per_backend"]}
            for b in current["backends"]["per_backend"]:
                if b["id"] in before and b.get("cpu_quota_millis"):
                    used[b["id"]] = used.get(b["id"], 0) + b["usage_usec"] - before[b["id"]]["usage_usec"]
                    available[b["id"]] = available.get(b["id"], 0) + seconds * 1e6 * b["cpu_quota_millis"] / 1000.0
        ids = {b["id"] for _, r in in_window for b in r["backends"]["per_backend"]}
        if expected and len(ids) != expected:
            problems.append({"window": minute, "backend": None, "reason": f"{len(ids)} backends in the cgroup samples, expected {expected}"})
        for backend in sorted(ids):
            if available.get(backend, 0) <= 0:
                problems.append({"window": minute, "backend": backend, "reason": "no CPU interval in the window"})
            else:
                fraction = used[backend] / available[backend]
                cpu_min[backend] = min(cpu_min.get(backend, fraction), fraction)
                if fraction < CPU_QUOTA_USED_FRACTION:
                    problems.append({"window": minute, "backend": backend, "reason": f"CPU used {fraction:.3f} of its quota, below {CPU_QUOTA_USED_FRACTION}"})
            held = [r_b["memory_current"] / r_b["memory_max"] for _, r in in_window for r_b in r["backends"]["per_backend"]
                    if r_b["id"] == backend and r_b.get("memory_max") and r_b.get("memory_current") is not None]
            if not held:
                problems.append({"window": minute, "backend": backend, "reason": "memory current or its limit unknown"})
            elif min(held) < MEMORY_HELD_FRACTION:
                problems.append({"window": minute, "backend": backend, "reason": f"memory current {min(held):.3f} of its limit, below {MEMORY_HELD_FRACTION}"})
    start, end = evidence.get("load_started_ms", 0), evidence.get("load_finished_ms", 0)
    for sample in rows:
        if start < sample["at_ms"] <= end:
            for b in sample["backends"]["per_backend"]:
                if b.get("memory_max") and b.get("memory_current") is not None:
                    fraction = b["memory_current"] / b["memory_max"]
                    memory_min[b["id"]] = min(memory_min.get(b["id"], fraction), fraction)
                    memory_peak[b["id"]] = max(memory_peak.get(b["id"], fraction), fraction)
                if b.get("pids_max"):
                    reached = max(b.get("pids_current") or 0, b.get("pids_peak") or 0)
                    pids_peak[b["id"]] = max(pids_peak.get(b["id"], 0.0), reached / b["pids_max"])
    achieved = {"cpu_window_min": _fractions(cpu_min), "memory_current_min": _fractions(memory_min),
                "memory_current_peak": _fractions(memory_peak), "pids_peak": _fractions(pids_peak)}
    held_check = _check("backends_held_their_limits_in_every_window", bool(bounds) and bool(rows) and not problems,
                        {"windows": len(bounds), "cpu_minimum_fraction_of_quota": CPU_QUOTA_USED_FRACTION,
                         "memory_minimum_fraction_of_limit": MEMORY_HELD_FRACTION, "problems": problems[:50], "problem_count": len(problems)})
    short = {b: f for b, f in pids_peak.items() if f < PIDS_REACHED_FRACTION}
    pid_check = _check("backends_reached_their_pid_limit", bool(pids_peak) and (not expected or len(pids_peak) == expected) and not short,
                       {"minimum_fraction_of_limit": PIDS_REACHED_FRACTION, "peak_fractions": pids_peak, "below_the_minimum": short})
    return held_check, pid_check, achieved


def _flood_checks(evidence):
    windows = evidence.get("windows", [])
    floods = [w.get("flood") or {} for w in windows]
    answered = [f.get("backend_requests_ok", 0) for f in floods]
    below = [w["minute"] for w, n in zip(windows, answered) if n < FLOOD_MIN_ANSWERED_PER_WINDOW]
    first = _check("message_flood_answered_in_every_window", bool(windows) and not below, {
        "answered": sum(answered), "busy_refusals": sum(f.get("backend_requests_busy", 0) for f in floods),
        "minimum_per_window": FLOOD_MIN_ANSWERED_PER_WINDOW, "windows_below_the_minimum": below[:200],
        "answered_by_window": answered[:500]})
    classes_by_window, bad = [], []
    for window, flood in zip(windows, floods):
        classes = dict(flood.get("classes") or {})
        classes_by_window.append(classes)
        untyped = {k: v for k, v in classes.items() if k not in TYPED_FLOOD_CLASSES and v}
        if untyped or (flood.get("backend_request_errors", 0) and not classes):
            bad.append(window["minute"])
    second = _check("message_flood_refusals_are_typed", bool(windows) and not bad, {
        "allowed_classes": list(TYPED_FLOOD_CLASSES), "windows_with_other_classes": bad[:200], "classes_by_window": classes_by_window[:500]})
    dead = [w["minute"] for w, f in zip(windows, floods)
            if not f.get("flood_alive") or f.get("heartbeat_age_ms", FLOOD_HEARTBEAT_MAX_AGE_MS + 1) > FLOOD_HEARTBEAT_MAX_AGE_MS]
    third = _check("message_flood_alive_throughout", bool(windows) and not dead,
                   {"maximum_heartbeat_age_ms": FLOOD_HEARTBEAT_MAX_AGE_MS, "windows_without_a_live_flood": dead[:200]})
    return [first, second, third]


def _probe_checks(probes, unit_tests):
    names = {p["name"] for p in probes}
    native = [p for p in probes if p.get("level") == "native"]
    unit = [p for p in probes if p.get("level") == "unit"]
    checks = [_check("every_required_probe_ran", set(REQUIRED_PROBES) <= names, {"missing": sorted(set(REQUIRED_PROBES) - names)})]
    failed = [{"name": p["name"], "expected": p.get("expected"), "observed": p.get("observed")} for p in native if not p.get("passed")]
    checks.append(_check("every_excess_request_refused_with_its_typed_error", native and not failed, {"native_probes": len(native), "failed": failed}))
    retired = {p["name"]: p.get("retired") for p in probes if p["name"] in RETIRED_PROBES}
    checks.append(_check("probes_retired_through_the_managed_uninstall",
                         set(retired) == set(RETIRED_PROBES) and all(v == "managed_uninstall" for v in retired.values()), {"retired": retired}))
    results = unit_tests or {}
    reported, problems = [], []
    for p in unit:
        tests = list(p.get("tests") or [])
        passed = bool(tests) and all(results.get(t) is True for t in tests)
        reported.append({"name": p["name"], "tests": tests, "tests_passed": passed})
        if p.get("observed") is not None:
            problems.append({"name": p["name"], "reason": "a unit level probe has no observation in this run"})
        if not tests:
            problems.append({"name": p["name"], "reason": "a unit level probe names the tests that prove it"})
        elif not passed:
            problems.append({"name": p["name"], "reason": "its tests did not pass in this job", "tests": {t: results.get(t) for t in tests}})
    checks.append(_check("unit_level_probe_tests_passed", not problems, {"unit_level": reported, "problems": problems}))
    return checks, reported


def _mean_shares(rows):
    if not rows:
        return {}
    return {name: round(sum(r.get(name, 0.0) for r in rows) / len(rows), 2) for name in ("host_busy",) + CPU_GROUPS}


def _flatten(prefix, value, out):
    if isinstance(value, dict):
        for key, inner in value.items():
            _flatten(f"{prefix}.{key}" if prefix else key, inner, out)
    elif isinstance(value, (int, float)) and not isinstance(value, bool):
        out[prefix] = value


def _stall_metrics(samples):
    """Every numeric stall reading of every sample, flat, plus each pressure file's stalled share of the interval since the previous sample."""
    rows, previous = [], None
    for sample in samples:
        flat = {}
        for key in ("pressure", "vm", "disk", "pg"):
            _flatten(key, sample.get(key) or {}, flat)
        if previous is not None and sample["at_ms"] > previous["at_ms"]:
            seconds = (sample["at_ms"] - previous["at_ms"]) / 1000.0
            for group, resources in (sample.get("pressure") or {}).items():
                for resource, kinds in resources.items():
                    for kind, reading in kinds.items():
                        before = ((previous.get("pressure") or {}).get(group, {}).get(resource, {}).get(kind) or {}).get("total_us")
                        if before is not None:
                            flat[f"pressure.{group}.{resource}.{kind}.stall_percent"] = round(100.0 * (reading["total_us"] - before) / (seconds * 1e6), 2)
        rows.append((sample["at_ms"], flat))
        previous = sample
    return rows


def _summarise(rows):
    names = {name for _, flat in rows for name in flat}
    return {name: {"mean": round(sum(f[name] for _, f in rows if name in f) / sum(1 for _, f in rows if name in f), 3),
                   "max": max(f[name] for _, f in rows if name in f)} for name in sorted(names)}


def stall_summary(evidence, samples):
    """Idle against loaded: pressure (avg10 and the stalled share of each interval), I/O wait, dirty pages, disk utilisation and await, and
    PostgreSQL's commit rate and mean WAL sync time, each as mean and max over the period."""
    rows = _stall_metrics(sorted(samples, key=lambda s: s["at_ms"]))
    idle_windows = evidence.get("idle_windows") or []
    idle = []
    if idle_windows:
        start, end = min(w.get("started_ms", 0) for w in idle_windows), max(w.get("ended_ms", 0) for w in idle_windows)
        idle = [r for r in rows if start < r[0] <= end]
    loaded = [r for r in rows if evidence.get("load_started_ms", 0) < r[0] <= evidence.get("load_finished_ms", 0)]
    return {"idle": _summarise(idle), "loaded": _summarise(loaded)}


def cpu_detail_top(evidence, samples, limit=15):
    """Mean share of the host over the load of each command inside docker and inside "other", largest first."""
    start, end = evidence.get("load_started_ms", 0), evidence.get("load_finished_ms", 0)
    rows = [s["cpu_detail"] for s in samples if s.get("cpu_detail") and start < s["at_ms"] <= end]
    top = {"docker": [], "other": []}
    if not rows:
        return top
    names = {k for r in rows for k in r}
    means = {k: sum(r.get(k, 0.0) for r in rows) / len(rows) for k in names}
    for group in top:
        top[group] = [{"name": k.split(".", 1)[1], "percent_of_host": round(v, 3)} for k, v in sorted(means.items(), key=lambda kv: -kv[1]) if k.startswith(group + ".")][:limit]
    return top


def cpu_modes_loaded(evidence, samples):
    start, end = evidence.get("load_started_ms", 0), evidence.get("load_finished_ms", 0)
    rows = [s["cpu_modes"] for s in samples if s.get("cpu_modes") and start < s["at_ms"] <= end]
    return {name: round(sum(r.get(name, 0.0) for r in rows) / len(rows), 2) for name in MODE_NAMES} if rows else {}


def per_cpu_summary(evidence, samples):
    """Each CPU's busy percent, mean and max, for the idle period and the load."""
    rows = [(s["at_ms"], dict(s["cpu_per_core"])) for s in sorted(samples, key=lambda s: s["at_ms"]) if s.get("cpu_per_core")]
    idle_windows = evidence.get("idle_windows") or []
    idle = []
    if idle_windows:
        start, end = min(w.get("started_ms", 0) for w in idle_windows), max(w.get("ended_ms", 0) for w in idle_windows)
        idle = [r for r in rows if start < r[0] <= end]
    loaded = [r for r in rows if evidence.get("load_started_ms", 0) < r[0] <= evidence.get("load_finished_ms", 0)]
    return {"idle": _summarise(idle), "loaded": _summarise(loaded)}


def cpu_breakdown(evidence, samples):
    """Each group's share of the host CPU (percent of all CPUs) for the idle period and for every loaded window, and the flood's
    admitted and refused request rates per second for every window."""
    with_groups = [s for s in samples if s.get("cpu_groups")]
    idle_windows = evidence.get("idle_windows") or []
    idle = []
    if idle_windows:
        start = min(w.get("started_ms", 0) for w in idle_windows)
        end = max(w.get("ended_ms", 0) for w in idle_windows)
        idle = [s["cpu_groups"] for s in with_groups if start < s["at_ms"] <= end]
    windows, rates = [], []
    for minute, start, end in _window_bounds(evidence):
        mean = _mean_shares([s["cpu_groups"] for s in with_groups if start < s["at_ms"] <= end])
        windows.append({"minute": minute, **mean})
    for window in evidence.get("windows", []):
        flood = window.get("flood") or {}
        span = max((window.get("ended_ms", 0) - window.get("started_ms", 0)) / 1000.0, 1.0)
        rates.append({"minute": window["minute"], "admitted_per_second": round(flood.get("backend_requests_ok", 0) / span, 1),
                      "refused_per_second": round(flood.get("backend_requests_busy", 0) / span, 1)})
    return {"idle": _mean_shares(idle), "windows": windows, "flood_rates": rates}


def evaluate(evidence, samples, parent_passed, failed_samples=0, unit_tests=None, variant="", cpu_percent="", reserved_cpus="", pin_platform=False, platform_weight=""):
    """Judge the Go evidence and the host samples against the pass criteria of doc 167 section 4."""
    # The Go side encodes an empty list as null, and a run that died early has no evidence at all.
    evidence = {k: v for k, v in (evidence or {}).items() if v is not None}
    checks = []
    probes = evidence.get("probes", [])
    probe_checks, unit_reported = _probe_checks(probes, unit_tests)
    checks.extend(probe_checks)
    checks.append(_check("no_runner_crash", parent_passed, "the Go acceptance, which owns the runner, ended cleanly" if parent_passed else "the Go acceptance failed"))
    checks.append(_check("load_completed", "load_finished" in evidence.get("markers", []), {"markers": evidence.get("markers", [])}))
    idle = evidence.get("idle", {})
    checks.append(_check("idle_baseline_measured", all(idle.get(s, {}).get("count", 0) > 0 for s in LATENCY_SERIES), idle))
    minutes = evidence.get("duration_minutes", 0)
    windows = evidence.get("windows", [])
    checks.append(_check("one_measurement_window_per_minute", minutes > 0 and len(windows) >= max(1, minutes - 1),
                         {"windows": len(windows), "minutes": minutes}))
    checks.append(_check("session_outlives_the_run", evidence.get("session_expires_ms", 0) > evidence.get("load_finished_ms", 0) > 0,
                         {"session_expires_ms": evidence.get("session_expires_ms"), "load_finished_ms": evidence.get("load_finished_ms")}))
    ratio_checks = {"home": _latency_check("home_p95_within_absolute_bound", "home", evidence),
                    "report": _latency_check("report_p95_within_absolute_bound", "report", evidence),
                    "dispatch": _latency_check("dispatch_within_threshold_of_idle", "dispatch", evidence),
                    "batch_start": _latency_check("batch_start_p95_within_absolute_bound", "batch_start", evidence)}
    checks.extend(ratio_checks.values())
    checks.extend(_flood_checks(evidence))

    budgets = evidence.get("budgets", {})
    checks.append(_loads_check(evidence))
    held_check, pid_check, achieved = _backend_limits(evidence, samples)
    checks.extend([held_check, pid_check])
    start, end = evidence.get("load_started_ms", 0), evidence.get("load_finished_ms", 0)
    load = [s for s in samples if start < s["at_ms"] <= end]
    expected_samples = int((end - start) / 1000.0 // SAMPLE_INTERVAL_SECONDS) if end > start else 0
    required = max(1, -(-int(HOST_SAMPLE_COVERAGE * 100) * expected_samples // 100))
    checks.append(_check("host_samples_cover_the_load", len(load) >= required and failed_samples * 10 <= expected_samples,
                         {"samples": len(load), "expected": expected_samples, "required": required, "failed_samples": failed_samples}))
    peak_memory = max((s["backends"]["memory_bytes"] for s in load), default=0)
    # CPU is judged on the kernel's counters, averaged over each interval, when they exist: the first sample has no
    # previous reading and falls back to a one-second rate that can read a few percent over an enforced quota.
    cpu_samples = [s for s in load if s["backends"].get("cpu_source") == "cgroup"] or load
    peak_cpu = max((s["backends"]["cpu_millis"] for s in cpu_samples), default=0.0)
    checks.append(_check("backend_memory_within_budget", load and peak_memory <= budgets.get("memory_mib", 0) * 1024 * 1024,
                         {"peak_bytes": peak_memory, "budget_mib": budgets.get("memory_mib")}))
    checks.append(_check("backend_cpu_within_budget", load and peak_cpu <= budgets.get("cpu_millis", 0),
                         {"peak_millis": round(peak_cpu, 1), "budget_millis": budgets.get("cpu_millis")}))
    # The container caps sit below the budgets, so those two cannot fail on their own: the host's free memory is judged too.
    lowest = min(load, key=lambda s: s["mem_available_kib"] / max(s["mem_total_kib"], 1), default=None)
    fraction = lowest["mem_available_kib"] / max(lowest["mem_total_kib"], 1) if lowest else 0.0
    checks.append(_check("host_memory_headroom", bool(load) and fraction >= HOST_MIN_AVAILABLE_FRACTION,
                         {"minimum_available_fraction": HOST_MIN_AVAILABLE_FRACTION, "lowest_available_fraction": round(fraction, 4),
                          "lowest_available_kib": lowest["mem_available_kib"] if lowest else None}))
    expected = budgets.get("running_backends", 0)
    crashes = [s["at_ms"] for s in load if s["backends"]["running"] != expected or s["backends"]["exited"] or s["backends"]["restarting"]
               or s["backends"]["oom_killed"] or s["backends"]["restart_count"]]
    checks.append(_check("no_backend_crash", load and not crashes, {"expected_running": expected, "bad_samples": crashes[:20]}))
    ratios = {series: check["detail"]["worst_ratio"] for series, check in ratio_checks.items()}
    # The shallow-backlog dispatch decision is recorded beside the judged one, never judged.
    shallow_idle = max(idle.get("dispatch_shallow", {}).get("p95_us", 0), 1)
    shallow = [w["dispatch_shallow"]["p95_us"] for w in windows if w.get("dispatch_shallow")]
    shallow_ratio = round(max(shallow) / shallow_idle, 3) if shallow else None
    busy_values = [s["host_cpu_percent"] for s in load if s.get("host_cpu_percent") is not None]
    busy = {"mean": round(sum(busy_values) / len(busy_values), 1), "max": max(busy_values)} if busy_values else {}
    backend_cpu_mean = round(sum(s["backends"]["cpu_millis"] for s in cpu_samples) / len(cpu_samples), 1) if cpu_samples else 0.0
    breakdown = cpu_breakdown(evidence, samples)
    passed = all(c["passed"] for c in checks)
    return {
        # A diagnostic variant measures the scenario with one part removed: it explains a result and is never evidence.
        "passed": passed, "counts_as_ops08_evidence": passed and minutes >= OPS08_MINUTES and not variant and evidence.get("db_driver") == "postgres"
        and cpu_percent in ("", str(RUNNER_DEFAULT_CPU_PERCENT)) and reserved_cpus in ("", str(RUNNER_DEFAULT_RESERVED_CPUS)) and not pin_platform and not platform_weight,
        "platform_cpu_weight": platform_weight,
        "pin_platform": pin_platform,
        "reserved_cpus_override": reserved_cpus,
        "cpu_percent_override": cpu_percent, "cpu_breakdown": breakdown, "stall_summary": stall_summary(evidence, samples), "per_cpu_busy": per_cpu_summary(evidence, samples), "cpu_detail_top": cpu_detail_top(evidence, samples), "cpu_modes_loaded": cpu_modes_loaded(evidence, samples), "host_busy_percent": busy, "backend_cpu_millis_mean": backend_cpu_mean,
        "variant": variant, "db_driver": evidence.get("db_driver") or "", "checks": checks,
        "unit_level_probes": unit_reported, "achieved_fractions": achieved,
        "worst_ratios": ratios, "informational_shallow_dispatch_worst_ratio": shallow_ratio, "peak_backend_memory_bytes": peak_memory, "peak_backend_cpu_millis": round(peak_cpu, 1),
    }


# ---- unit test results ----

def parse_go_test_json(text):
    """name -> passed, from `go test -json` output; a skipped or failed test is not a pass. Package lines have no test."""
    results = {}
    for line in text.splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if isinstance(event, dict) and event.get("Test") and event.get("Action") in ("pass", "fail", "skip"):
            results[event["Test"]] = event["Action"] == "pass"
    return results


def read_go_test_json(path):
    try:
        return parse_go_test_json(Path(path).read_text())
    except (OSError, ValueError):
        return {}


# ---- configuration ----

def soak_image(value):
    match = re.fullmatch(r"localhost:([0-9]{1,5})/portal-soak-backend@sha256:[a-f0-9]{64}", value or "")
    if not match or not 1 <= int(match.group(1)) <= 65535:
        raise RuntimeError("the soak backend image must be an owned loopback digest reference")
    return value


def duration_minutes(value):
    if not re.fullmatch(r"[0-9]+", value or "") or not 1 <= int(value) <= MAX_DURATION_MINUTES:
        raise RuntimeError(f"the duration is 1 to {MAX_DURATION_MINUTES} minutes")
    return int(value)


def soak_variant(value):
    """The diagnostic variants of the Go soak: a closed list, empty for an evidence run."""
    if not re.fullmatch(r"(memory-db|no-batch-probes|skip-image-store)(,(memory-db|no-batch-probes|skip-image-store))*|", value or ""):
        raise RuntimeError("the soak variant is empty or a list of memory-db and no-batch-probes")
    return value or ""


def soak_cpu_percent(value):
    """An override of the runner's capacity.cpu_percent for a diagnostic run: empty or an integer 1 to 100."""
    if value and not (re.fullmatch(r"[0-9]{1,3}", value) and 1 <= int(value) <= 100 and str(int(value)) == value):
        raise RuntimeError("soak_cpu_percent is empty or an integer from 1 to 100")
    return value or ""


def soak_reserved_cpus(value):
    """An override of the runner's capacity.reserved_cpus for a diagnostic run: empty or a non-negative integer."""
    if value and not (re.fullmatch(r"[0-9]{1,2}", value) and int(value) <= 64 and str(int(value)) == value):
        raise RuntimeError("soak_reserved_cpus is empty or an integer from 0 to 64")
    return value or ""


def soak_platform_cpu_weight(value):
    """CPUWeight for the Portal scope and the PostgreSQL unit: empty or an integer 1 to 10000 (a diagnostic, never evidence)."""
    if value and not (re.fullmatch(r"[0-9]{1,5}", value) and 1 <= int(value) <= 10000 and str(int(value)) == value):
        raise RuntimeError("soak_platform_cpu_weight is empty or an integer from 1 to 10000")
    return value or ""


def soak_pin_platform(value):
    """Whether the Portal scope, PostgreSQL and the runner are confined to the reserved CPUs (a diagnostic, never evidence)."""
    if value not in ("", "true", "false"):
        raise RuntimeError("soak_pin_platform is true or false")
    return value == "true"


def pin_cpu_set(reserved_override):
    """The CPU list of the reserved CPUs, which the runner takes from the bottom: 1 is "0", 2 is "0-1"."""
    reserved = int(reserved_override or RUNNER_DEFAULT_RESERVED_CPUS)
    if reserved < 1:
        raise RuntimeError("no CPU is reserved, so there is nothing to pin the platform to")
    return "0" if reserved == 1 else f"0-{reserved - 1}"


def postgres_unit(run):
    for line in run("systemctl", "list-units", "postgresql@*.service", "--no-legend", "--plain").splitlines():
        if line.split() and line.split()[0].startswith("postgresql@"):
            return line.split()[0]
    raise RuntimeError("no postgresql@ unit is running")


def soak_database(value):
    """The measured Portal's database: PostgreSQL (the evidence shape, owner decision 2026-11-14) or the file-backed SQLite variant."""
    if value not in ("", "postgres", "sqlite-file"):
        raise RuntimeError("the soak database is postgres or sqlite-file")
    return value or "postgres"


def image_store_mode(free_bytes):
    """Whether the image store probe runs natively: two images of about 4 GiB each are loaded into the daemon, so its disk
    must hold 8 GiB and keep headroom. Otherwise the probe stays at unit level and says why, with the measured free space."""
    if free_bytes is not None and free_bytes >= IMAGE_STORE_NATIVE_BYTES:
        return "native", ""
    return "unit", (f"the Docker store's disk had {free_bytes} bytes free; two 4 GiB images need 8 GiB and the run keeps "
                    f"{IMAGE_STORE_HEADROOM_BYTES} bytes of headroom, {IMAGE_STORE_NATIVE_BYTES} bytes in all")


def baseline_seconds(minutes):
    """The idle baseline is three minutes in every run: the pull request smoke differs from the dispatch run in duration (and in
    the diagnosis it adds), not in how it measures."""
    return 180


def require_disposable_environment(env, euid, platform, which):
    if platform != "linux" or euid != 0:
        raise RuntimeError("the soak requires disposable Linux root")
    if env.get("GITHUB_ACTIONS") != "true" or env.get("RUNNER_ENVIRONMENT") != "github-hosted":
        raise RuntimeError("the soak refuses hosts outside a disposable GitHub-hosted worker")
    for tool in ("losetup", "blockdev", "mkfs.xfs", "mount", "umount", "findmnt", "xfs_quota", "blkid", "docker"):
        if not which(tool, path=PATH):
            raise RuntimeError("missing explicit acceptance tool: " + tool)


# ---- the run ----

def run_command(*args):
    result = subprocess.run(args, env=ENV, check=False, capture_output=True, text=True, timeout=120)
    if result.returncode:
        raise RuntimeError(f"{Path(args[0]).name} failed ({result.returncode}): {result.stderr[:2048]}")
    return result.stdout


def sampling_loop(sampler, stop, path, interval=SAMPLE_INTERVAL_SECONDS):
    """Samples until stopped. A failed sample is recorded as an error row and counted, so a gap is never silent:
    returns (samples, failed)."""
    samples, failed = [], 0
    with open(path, "a", encoding="utf-8") as sink:
        while not stop.is_set():
            began = time.time()
            try:
                row = sampler.sample()
                samples.append(row)
            except Exception as error:  # noqa: BLE001 - the loop must outlive one bad sample
                row = {"at_ms": int(began * 1000), "error": str(error)[:500]}
                failed += 1
            sink.write(json.dumps(row, sort_keys=True) + "\n")
            sink.flush()
            stop.wait(max(0.0, interval - (time.time() - began)))
    return samples, failed


def write_container_diagnostics(image_id, path):
    """State and the last log lines of every container of the soak image: why a backend stopped, if one did."""
    lines = []
    try:
        for container in run_command("docker", "ps", "-a", "--filter", "ancestor=" + image_id, "--format", "{{.ID}}").split():
            state = run_command("docker", "inspect", "--format", "{{json .State}}", container).strip()
            logs = subprocess.run(["docker", "logs", "--tail", "5", container], env=ENV, capture_output=True, text=True, timeout=60)
            lines += [f"== {container} {state}", (logs.stdout + logs.stderr)[-1500:]]
    except (RuntimeError, subprocess.SubprocessError) as error:
        lines.append("diagnostics unavailable: " + str(error))
    path.write_text("\n".join(lines))


def read_json_lines(path):
    try:
        return [json.loads(line) for line in Path(path).read_text().splitlines() if line.strip()]
    except OSError:
        return []


def main(argv):
    import argparse
    sys.path.insert(0, str(Path(__file__).resolve().parent))
    import project_quota_probe as probe  # the loop and mount ownership checks are shared, not copied

    parser = argparse.ArgumentParser()
    parser.add_argument("--duration-minutes", required=True)
    parser.add_argument("--portal-binary", required=True)
    parser.add_argument("--journal-binary", required=True)
    parser.add_argument("--out", required=True)
    parser.add_argument("--unit-tests", default="", help="go test -json output of the unit tests the unit level probes cite")
    args = parser.parse_args(argv)
    minutes = duration_minutes(args.duration_minutes)
    image = soak_image(os.environ.get("APP_CAPACITY_SOAK_IMAGE"))
    variant = soak_variant(os.environ.get("APP_CAPACITY_SOAK_VARIANT", ""))
    cpu_percent = soak_cpu_percent(os.environ.get("APP_CAPACITY_SOAK_CPU_PERCENT", ""))
    reserved_cpus = soak_reserved_cpus(os.environ.get("APP_CAPACITY_SOAK_RESERVED_CPUS", ""))
    platform_weight = soak_platform_cpu_weight(os.environ.get("APP_CAPACITY_SOAK_PLATFORM_CPU_WEIGHT", ""))
    pin_platform = soak_pin_platform(os.environ.get("APP_CAPACITY_SOAK_PIN_PLATFORM", ""))
    database = soak_database(os.environ.get("APP_CAPACITY_SOAK_DB", ""))
    app_binary = os.environ.get("APP_CAPACITY_SOAK_APP_BINARY", "")
    for path in (args.portal_binary, args.journal_binary, args.out) + ((app_binary,) if app_binary else ()):
        if not Path(path).is_absolute():
            raise RuntimeError("paths must be absolute")
    require_disposable_environment(os.environ, os.geteuid(), sys.platform, shutil.which)
    require_systemd_scopes(run_command)
    pin_cpus = ""
    if platform_weight:  # PostgreSQL's unit gets the same weight as the Portal's scope; the runner's scope keeps the default
        run_command("systemctl", "set-property", "--runtime", postgres_unit(run_command), "CPUWeight=" + platform_weight)
    if pin_platform:
        pin_cpus = pin_cpu_set(reserved_cpus)
        # PostgreSQL is a unit of the runner image: its cpuset is set at run time, before the soak starts, and only for this run.
        run_command("systemctl", "set-property", "--runtime", postgres_unit(run_command), "AllowedCPUs=" + pin_cpus)
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    socket = os.path.realpath("/var/run/docker.sock")
    image_id = run_command("docker", "image", "inspect", image, "--format", "{{.Id}}").strip()
    if not re.fullmatch(r"sha256:[a-f0-9]{64}", image_id):
        raise RuntimeError("the soak image is not in the local Docker store")

    # The daemon store's disk decides whether the image store probe can run natively (two 4 GiB images); measured, not assumed.
    docker_root = run_command("docker", "info", "--format", "{{.DockerRootDir}}").strip() or "/var/lib/docker"
    try:
        docker_free = shutil.disk_usage(docker_root).free
    except OSError:
        docker_free = None
    store_mode, store_reason = image_store_mode(docker_free)
    print(f"docker store {docker_root}: {docker_free} bytes free; image store probe: {store_mode}", file=sys.stderr)
    unit_tests = read_go_test_json(args.unit_tests) if args.unit_tests else {}

    root = Path(tempfile.mkdtemp(prefix="portal-capacity-soak-", dir="/tmp"))
    root.chmod(0o755)
    volume = root / "owned.xfs"
    mountpoint = root / "mount"
    mountpoint.mkdir()
    loop = None
    parent_passed, evidence, samples, started = False, {}, [], time.time()
    failed_counter = [0]
    try:
        with volume.open("xb") as stream:
            stream.truncate(VOLUME_BYTES)  # sparse: only written blocks use the worker's disk
        volume.chmod(0o600)
        loop = probe.command("losetup", "--find", "--show", str(volume))
        probe.require_loop_owner(loop, volume)
        probe.command("mkfs.xfs", "-f", loop)
        probe.command("mount", "-t", "xfs", "-o", "prjquota,nodev,nosuid,noexec", loop, str(mountpoint))
        probe.require_mount_owner(mountpoint, loop)
        state = probe.command("xfs_quota", "-x", "-c", "state -p", str(mountpoint))
        if "Accounting: ON" not in state or "Enforcement: ON" not in state:
            raise RuntimeError("project quota accounting and enforcement are not both enabled")

        journal_root = mountpoint / "journal-data"
        journal_root.mkdir(mode=0o700)
        control = root / "journal-control"
        control.mkdir(mode=0o700)
        fixture = root / "portal-fixture"
        fixture.mkdir(mode=0o755)
        native = dict(ENV, GITHUB_ACTIONS="true", RUNNER_ENVIRONMENT="github-hosted",
                      APP_DATA_NATIVE_UUID=probe.command("blkid", "-p", "-s", "UUID", "-o", "value", loop),
                      APP_DATA_NATIVE_PHASE="prepare", APP_DATA_JOURNAL_ROOT=str(journal_root),
                      APP_DATA_JOURNAL_CONTROL=str(control), APP_DATA_JOURNAL_INODE=str(journal_root.stat().st_ino))
        prepared = subprocess.run([args.journal_binary, "-test.run=^TestNativeJournalDataExecution$", "-test.v", "-test.timeout=60s"],
                                  env=native, capture_output=True, text=True, timeout=70)
        if prepared.returncode or "--- PASS: TestNativeJournalDataExecution" not in prepared.stdout:
            raise RuntimeError("journal preparation failed: " + (prepared.stdout + prepared.stderr)[-16384:])

        soak_env = dict(native, APP_CANDIDATE_DOCKER_SOCKET=socket, APP_DATA_PORTAL_FIXTURE=str(fixture),
                        APP_CAPACITY_SOAK_IMAGE=image, APP_CAPACITY_SOAK_OUT=str(out),
                        APP_CAPACITY_SOAK_DESCRIPTORS=str(Path(__file__).resolve().parents[2] / "internal/appplatform/testdata/descriptors"),
                        APP_CAPACITY_SOAK_MINUTES=str(minutes), APP_CAPACITY_SOAK_BASELINE_SECONDS=str(baseline_seconds(minutes)),
                        APP_CAPACITY_SOAK_IMAGE_STORE=store_mode, APP_CAPACITY_SOAK_IMAGE_STORE_REASON=store_reason,
                        APP_CAPACITY_SOAK_DOCKER_FREE_BYTES=str(docker_free if docker_free is not None else ""),
                        APP_CAPACITY_SOAK_APP_BINARY=app_binary, APP_CAPACITY_SOAK_VARIANT=variant, APP_CAPACITY_SOAK_DB=database, APP_CAPACITY_SOAK_CPU_PERCENT=cpu_percent, APP_CAPACITY_SOAK_RESERVED_CPUS=reserved_cpus, APP_CAPACITY_SOAK_PIN_CPUS=pin_cpus, APP_CAPACITY_SOAK_PLATFORM_CPU_WEIGHT=platform_weight, APP_CAPACITY_SOAK_SCOPES="systemd",
                        APP_CAPACITY_SOAK_POSTGRES_DSN=os.environ.get("APP_CAPACITY_SOAK_POSTGRES_DSN", ""))
        soak_env.pop("APP_DATA_NATIVE_PHASE")
        if os.environ.get("APP_CAPACITY_SOAK_DIAGNOSE") == "1":
            soak_env["APP_CAPACITY_SOAK_DIAGNOSE"] = "1"
        if os.environ.get("APP_CAPACITY_SOAK_RUNNER_PROFILE") == "1":
            soak_env["APP_CAPACITY_SOAK_RUNNER_PROFILE"] = "1"
        devices, extra = {}, {}
        try:
            data_dir = run_command("runuser", "-u", "postgres", "--", "psql", "-tAXc", "SHOW data_directory").strip()
            extra["postgres_data_dir"] = data_dir
            devices["postgres_data"] = device_number(data_dir)
        except (RuntimeError, OSError, subprocess.SubprocessError):
            pass  # not a PostgreSQL run
        devices["xfs_mount"], devices["xfs_backing_file"], devices["docker_root"] = device_number(mountpoint), device_number(volume), device_number(docker_root)
        storage = storage_layout(devices, parse_diskstats(Path("/proc/diskstats").read_text()), extra)
        print("storage layout:", json.dumps(storage, sort_keys=True), file=sys.stderr)
        tracked = {name: dev for name, dev in devices.items() if name != "docker_root"}
        sampler = Sampler(run_command, "/proc", mountpoint, image_id, disk_devices=tracked, postgres="postgres_data" in devices)
        stop = threading.Event()
        collected, failed_counter = [], [0]

        def sample_until_stopped():
            samples_taken, failures = sampling_loop(sampler, stop, out / "samples.jsonl")
            collected.extend(samples_taken)
            failed_counter[0] = failures

        worker = threading.Thread(target=sample_until_stopped, daemon=True)
        worker.start()
        started = time.time()
        log = out / "portal-parent.log"
        with log.open("w", encoding="utf-8") as sink:
            result = subprocess.run([args.portal_binary, "-test.run=^TestPlatformCapacitySoak$", "-test.v", f"-test.timeout={minutes + 80}m"],
                                    env=soak_env, stdout=sink, stderr=subprocess.STDOUT, timeout=(minutes + 85) * 60)
        stop.set()
        worker.join(timeout=SAMPLE_INTERVAL_SECONDS + 60)
        write_container_diagnostics(image_id, out / "containers.txt")
        samples = list(collected)
        parent_passed = result.returncode == 0 and "--- PASS: TestPlatformCapacitySoak" in log.read_text()
        try:
            evidence = json.loads((out / "child-evidence.json").read_text())
        except (OSError, ValueError):
            evidence = {}
    finally:
        # Never unmount, detach or delete a resource whose ownership changed; on cleanup failure leave it to the
        # disposable worker's teardown.
        if loop is None and volume.exists():
            loop = probe.discover_owned_loop(volume)
        deferred = False
        if loop is not None:
            probe.require_loop_owner(loop, volume)
            if probe.owned_mount_present(mountpoint, loop):
                probe.command("umount", str(mountpoint))
            probe.require_loop_owner(loop, volume)
            probe.command("losetup", "--detach", loop)
            deferred = probe.discover_owned_loop(volume) is not None
        if not deferred:
            shutil.rmtree(root, ignore_errors=True)

    verdict = evaluate(evidence, samples, parent_passed, failed_samples=failed_counter[0], unit_tests=unit_tests, variant=variant, cpu_percent=cpu_percent, reserved_cpus=reserved_cpus, pin_platform=pin_platform, platform_weight=platform_weight)
    for name in ("portal-parent.log", "containers.txt"):
        try:
            print(f"--- tail of {name} ---\n" + (out / name).read_text(errors="replace")[-6000:], file=sys.stderr)
        except OSError:
            pass
    report = {
        "format": EVIDENCE_FORMAT, "requested_minutes": minutes, "elapsed_seconds": int(time.time() - started),
        "ops08_minutes_required": OPS08_MINUTES,
        # The branch and the commit the run measured, and how it was started: a dispatch of the reviewed branch at that commit.
        "run": {key: os.environ.get(key, "") for key in ("GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT", "GITHUB_SERVER_URL", "GITHUB_REPOSITORY", "GITHUB_SHA",
                                                           "GITHUB_REF", "GITHUB_REF_NAME", "GITHUB_EVENT_NAME", "GITHUB_WORKFLOW_REF")},
        "host": {"kernel": os.uname().release, "architecture": os.uname().machine, "cpus": os.cpu_count(),
                 "volume_bytes": VOLUME_BYTES, "image": image, "image_id": image_id, "docker_root": docker_root,
                 "docker_root_free_bytes": docker_free, "image_store_probe": store_mode, "image_store_probe_reason": store_reason},
        "unit_tests": unit_tests, "runner_cpus": parse_ready_cpus((out / "runner.log").read_text(errors="replace")) if (out / "runner.log").exists() else {},
        "backend_cpusets": {r["id"]: r.get("cpuset_effective") for sample in samples[-1:] for r in (sample.get("backends") or {}).get("per_backend", [])},
        "storage_layout": storage if "storage" in locals() else {}, "cgroup_layout": sampler.layout if "sampler" in locals() else {}, "variant": variant, "database": database, "runner_cpu_percent_override": cpu_percent, "runner_reserved_cpus_override": reserved_cpus, "pin_platform_cpus": pin_cpus, "topology": collect_topology(run_command),
        "child": evidence, "samples": samples, "runner_log": read_json_lines(out / "runner-log.jsonl"), "verdict": verdict,
    }
    (out / "capacity-soak-evidence.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
    summary = {"passed": verdict["passed"], "counts_as_ops08_evidence": verdict["counts_as_ops08_evidence"],
               "failed_checks": [c["name"] for c in verdict["checks"] if not c["passed"]], "worst_ratios": verdict["worst_ratios"],
               "achieved_fractions": {name: {"min": value.get("min"), "max": value.get("max")} for name, value in verdict["achieved_fractions"].items()}, "shallow_dispatch_worst_ratio": verdict["informational_shallow_dispatch_worst_ratio"],
               "peak_backend_memory_bytes": verdict["peak_backend_memory_bytes"], "peak_backend_cpu_millis": verdict["peak_backend_cpu_millis"]}
    print("cgroup layout:", json.dumps(report["host"].get("cgroup_layout") or report.get("cgroup_layout"), sort_keys=True))
    print(json.dumps(summary, indent=2, sort_keys=True))
    step_summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if step_summary:
        with open(step_summary, "a", encoding="utf-8") as sink:
            sink.write(render_summary(evidence, verdict))
    return 0 if verdict["passed"] else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
