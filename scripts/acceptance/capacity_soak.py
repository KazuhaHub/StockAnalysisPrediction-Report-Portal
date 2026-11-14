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


# ---- sampling ----

class Sampler:
    """One host sample: memory, CPU, tasks, volume, container logs, and the sum over the soak backends.

    `run` runs a command and returns its text (injected so the parsing is tested without a Docker daemon)."""

    def __init__(self, run, proc, mountpoint, image_id, clock=time.time, cgroup_root="/sys/fs/cgroup/system.slice"):
        self.run, self.proc, self.mountpoint, self.image_id, self.clock = run, Path(proc), Path(mountpoint), image_id, clock
        self.cgroup_root = Path(cgroup_root)
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

    def sample(self):
        now = self.clock()
        memory = parse_meminfo((self.proc / "meminfo").read_text())
        cpu = parse_proc_stat((self.proc / "stat").read_text())
        percent = None if self._cpu is None else cpu_percent(self._cpu, cpu)
        self._cpu = cpu
        running_tasks, total_tasks = parse_loadavg_tasks((self.proc / "loadavg").read_text())
        volume = os.statvfs(self.mountpoint)
        backends, log_bytes = self._backends(now)
        return {
            "at_ms": int(now * 1000), "mem_total_kib": memory["total_kib"], "mem_available_kib": memory["available_kib"],
            "host_cpu_percent": percent, "tasks_running": running_tasks, "tasks_total": total_tasks,
            "xfs_total_bytes": volume.f_blocks * volume.f_frsize,
            "xfs_used_bytes": (volume.f_blocks - volume.f_bfree) * volume.f_frsize,
            "docker_log_bytes": log_bytes, "backends": backends,
        }


# ---- judging ----

def _check(name, passed, detail):
    return {"name": name, "passed": bool(passed), "detail": detail}


# Owner decision 2026-11-13: the p95 of the loaded period must be within max(1.5x the idle p95, the idle p95 plus
# 5 ms). A ratio alone is meaningless against a sub-millisecond baseline; the load is generated outside the measured
# process, so the measured process sees only platform load. The owner decided only that floor.
#
# Verdict computation (engineering decision, not the owner's): the p95 of every loaded sample of the run pooled together
# (the idle p95 is pooled the same way). The p95 of each one-minute window, its threshold, the count over it and the worst
# window are diagnostics: none is hidden or dropped, none decides.
LATENCY_FLOOR_US = 5000
LATENCY_SERIES = ("home", "report", "dispatch", "batch_start")


def latency_threshold_us(idle_p95_us):
    return max(idle_p95_us * RATIO_LIMIT_NUMERATOR // RATIO_LIMIT_DENOMINATOR, idle_p95_us + LATENCY_FLOOR_US)


def _latency_check(name, series, evidence):
    baseline = max(evidence.get("idle", {}).get(series, {}).get("p95_us", 0), 0)
    threshold = latency_threshold_us(baseline)
    pooled = evidence.get("loaded", {}).get(series, {})
    pooled_p95, pooled_count = pooled.get("p95_us", 0), pooled.get("count", 0)
    rows, over, empty, worst = [], [], [], None
    for window in evidence.get("windows", []):
        stats = window.get(series) or {}
        p95 = stats.get("p95_us", 0)
        ok = p95 <= threshold
        rows.append({"minute": window["minute"], "p95_us": p95, "threshold_us": threshold, "passed": ok})
        if not ok:
            over.append(window["minute"])
        if not stats.get("count", 0):
            empty.append(window["minute"])
        if worst is None or p95 > worst["p95_us"]:
            worst = {"minute": window["minute"], "p95_us": p95}
    return _check(name, pooled_count > 0 and pooled_p95 <= threshold and not empty, {
        "idle_p95_us": baseline, "threshold_us": threshold, "pooled_p95_us": pooled_p95, "pooled_samples": pooled_count,
        "pooled_ratio": round(pooled_p95 / max(baseline, 1), 3), "worst_ratio": round((worst or {"p95_us": 0})["p95_us"] / max(baseline, 1), 3),
        "worst_window": worst, "windows": len(rows), "windows_over_count": len(over), "windows_over_the_threshold": over[:200],
        "windows_without_samples": empty[:200], "window_thresholds": rows})


def render_summary(evidence, verdict):
    """Markdown for the job's step summary: the verdict per series and every window of every series."""
    lines = [f"## Capacity soak ({evidence.get('duration_minutes')} minutes): {'passed' if verdict['passed'] else 'FAILED'}",
             "", "Latency verdict (engineering decision, not the owner's): the pooled loaded p95 must be at most "
             "max(1.5 x idle p95, idle p95 + 5 ms). Windows are diagnostics."]
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


def evaluate(evidence, samples, parent_passed, failed_samples=0, unit_tests=None, variant="", cpu_percent=""):
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
    ratio_checks = {"home": _latency_check("home_p95_within_threshold_of_idle", "home", evidence),
                    "report": _latency_check("report_p95_within_threshold_of_idle", "report", evidence),
                    "dispatch": _latency_check("dispatch_within_threshold_of_idle", "dispatch", evidence),
                    "batch_start": _latency_check("batch_start_within_threshold_of_idle", "batch_start", evidence)}
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
    passed = all(c["passed"] for c in checks)
    return {
        # A diagnostic variant measures the scenario with one part removed: it explains a result and is never evidence.
        "passed": passed, "counts_as_ops08_evidence": passed and minutes >= OPS08_MINUTES and not variant and evidence.get("db_driver") == "postgres"
        and cpu_percent in ("", str(RUNNER_DEFAULT_CPU_PERCENT)),
        "cpu_percent_override": cpu_percent, "host_busy_percent": busy, "backend_cpu_millis_mean": backend_cpu_mean,
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
    database = soak_database(os.environ.get("APP_CAPACITY_SOAK_DB", ""))
    app_binary = os.environ.get("APP_CAPACITY_SOAK_APP_BINARY", "")
    for path in (args.portal_binary, args.journal_binary, args.out) + ((app_binary,) if app_binary else ()):
        if not Path(path).is_absolute():
            raise RuntimeError("paths must be absolute")
    require_disposable_environment(os.environ, os.geteuid(), sys.platform, shutil.which)
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
                        APP_CAPACITY_SOAK_APP_BINARY=app_binary, APP_CAPACITY_SOAK_VARIANT=variant, APP_CAPACITY_SOAK_DB=database, APP_CAPACITY_SOAK_CPU_PERCENT=cpu_percent,
                        APP_CAPACITY_SOAK_POSTGRES_DSN=os.environ.get("APP_CAPACITY_SOAK_POSTGRES_DSN", ""))
        soak_env.pop("APP_DATA_NATIVE_PHASE")
        if os.environ.get("APP_CAPACITY_SOAK_DIAGNOSE") == "1":
            soak_env["APP_CAPACITY_SOAK_DIAGNOSE"] = "1"
        sampler = Sampler(run_command, "/proc", mountpoint, image_id)
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

    verdict = evaluate(evidence, samples, parent_passed, failed_samples=failed_counter[0], unit_tests=unit_tests, variant=variant, cpu_percent=cpu_percent)
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
        "unit_tests": unit_tests, "variant": variant, "database": database, "runner_cpu_percent_override": cpu_percent,
        "child": evidence, "samples": samples, "runner_log": read_json_lines(out / "runner-log.jsonl"), "verdict": verdict,
    }
    (out / "capacity-soak-evidence.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
    summary = {"passed": verdict["passed"], "counts_as_ops08_evidence": verdict["counts_as_ops08_evidence"],
               "failed_checks": [c["name"] for c in verdict["checks"] if not c["passed"]], "worst_ratios": verdict["worst_ratios"],
               "achieved_fractions": {name: {"min": value.get("min"), "max": value.get("max")} for name, value in verdict["achieved_fractions"].items()}, "shallow_dispatch_worst_ratio": verdict["informational_shallow_dispatch_worst_ratio"],
               "peak_backend_memory_bytes": verdict["peak_backend_memory_bytes"], "peak_backend_cpu_millis": verdict["peak_backend_cpu_millis"]}
    print(json.dumps(summary, indent=2, sort_keys=True))
    step_summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if step_summary:
        with open(step_summary, "a", encoding="utf-8") as sink:
            sink.write(render_summary(evidence, verdict))
    return 0 if verdict["passed"] else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
