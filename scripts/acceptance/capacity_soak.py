#!/usr/bin/env python3
"""Declared-capacity soak on a disposable GitHub-hosted worker (doc 171, PR 4b; OPS-08, closing SEC-09).

It builds a newly owned 16 GiB XFS project-quota loop volume, prepares the runner's journal on it, and runs the
Go acceptance TestPlatformCapacitySoak (a real root runner, a Portal child, fifty installations, the excess probes
and the platform floods) while this process samples the host every thirty seconds: memory, CPU, tasks, the Docker
and runner logs, the XFS volume and the summed memory and CPU of the soak backends. It then judges the Go
evidence and the samples together against the pass criteria of doc 167 section 4 and writes everything as JSON.

Only a run of at least 120 minutes that passes counts as OPS-08 evidence; a shorter run is a smoke of the same
harness. Only synthetic fixtures live on the volume, and only the loop device and mount this process created are
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
SAMPLE_INTERVAL_SECONDS = 30
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
            cgroup = self._cgroup_cpu_millis(running, full_ids, now)
            if cgroup is not None:
                summary["cpu_millis"], summary["cpu_source"] = cgroup, "cgroup"
        return summary, log_bytes

    def _cgroup_cpu_millis(self, running, full_ids, now):
        """The summed average CPU of the running backends since the previous sample, from the kernel's counters.

        This is the figure the budget is judged on when it is available: a rate sampled over a second can read a
        few percent above a quota that the kernel enforces exactly over any longer window."""
        current = {}
        for container in running:
            if container not in full_ids:
                return None
            try:
                usage = parse_cgroup_usage_usec((self.cgroup_root / f"docker-{full_ids[container]}.scope" / "cpu.stat").read_text())
            except (OSError, ValueError):
                return None
            current[container] = (usage, now)
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


# Owner decision 2026-11-13: a window passes when its p95 is within max(1.5x the idle p95, the idle p95 plus
# 5 ms). A ratio alone is meaningless against a sub-millisecond baseline; the load is generated outside the
# measured process, so the measured process sees only platform load.
LATENCY_FLOOR_US = 5000


def latency_threshold_us(idle_p95_us):
    return max(idle_p95_us * RATIO_LIMIT_NUMERATOR // RATIO_LIMIT_DENOMINATOR, idle_p95_us + LATENCY_FLOOR_US)


def _latency_check(name, series, evidence):
    baseline = max(evidence.get("idle", {}).get(series, {}).get("p95_us", 0), 0)
    threshold = latency_threshold_us(baseline)
    rows, over, worst = [], [], 0
    for window in evidence.get("windows", []):
        p95 = window[series]["p95_us"]
        worst = max(worst, p95)
        ok = p95 <= threshold
        rows.append({"minute": window["minute"], "p95_us": p95, "threshold_us": threshold, "passed": ok})
        if not ok:
            over.append(window["minute"])
    return _check(name, not over and rows, {
        "idle_p95_us": baseline, "threshold_us": threshold, "worst_window_p95_us": worst,
        "worst_ratio": round(worst / max(baseline, 1), 3), "windows_over_the_threshold": over[:20], "window_thresholds": rows})


def _loads_check(evidence):
    """The last window's status of each backend shows its own loads ran: memory held, CPU burners, threads, log
    written, and the data namespace filled to its quota. Without this a backend that did nothing would pass."""
    # Per backend, its latest status that answered: one missed request at the last minute is not a backend that did
    # nothing, and a backend that never answered is.
    latest = {}
    for window in evidence.get("windows", []):
        for b in window.get("backends", []):
            if b.get("answered") or b.get("installation") not in latest:
                latest[b.get("installation")] = b
    backends = list(latest.values())
    held_minimum = 0.5 * evidence.get("sizing", {}).get("MemoryMiB", 0) * 1024 * 1024
    expected = evidence.get("budgets", {}).get("running_backends", 0)
    problems = []
    for b in backends:
        reasons = []
        if not b.get("answered"):
            reasons.append("did not answer")
        else:
            if b["held_bytes"] < held_minimum or held_minimum <= 0:
                reasons.append("held too little memory")
            if b["threads"] < 8:
                reasons.append("started too few threads")
            if b["burners"] < 1:
                reasons.append("burned no CPU")
            if b["log_bytes"] <= 0:
                reasons.append("wrote no log")
            if not b["data_present"]:
                reasons.append("had no data namespace")
            elif b["storage_full"] < 1:
                reasons.append("never filled its data namespace")
        if reasons:
            problems.append({"installation": b.get("installation"), "reasons": reasons})
    return _check("backends_ran_every_declared_load", expected > 0 and len(backends) == expected and not problems,
                  {"backends": len(backends), "expected": expected, "problems": problems})


def evaluate(evidence, samples, parent_passed, min_samples=1):
    """Judge the Go evidence and the host samples against the pass criteria of doc 167 section 4."""
    # The Go side encodes an empty list as null, and a run that died early has no evidence at all.
    evidence = {k: v for k, v in (evidence or {}).items() if v is not None}
    checks = []
    probes = evidence.get("probes", [])
    names = {p["name"] for p in probes}
    checks.append(_check("every_required_probe_ran", set(REQUIRED_PROBES) <= names,
                         {"missing": sorted(set(REQUIRED_PROBES) - names)}))
    failed = [{"name": p["name"], "expected": p.get("expected"), "observed": p.get("observed")} for p in probes if not p.get("passed")]
    checks.append(_check("every_excess_request_refused_with_its_typed_error", probes and not failed, {"failed": failed}))
    checks.append(_check("no_runner_crash", parent_passed, "the Go acceptance, which owns the runner, ended cleanly" if parent_passed else "the Go acceptance failed"))
    checks.append(_check("load_completed", "load_finished" in evidence.get("markers", []), {"markers": evidence.get("markers", [])}))
    idle = evidence.get("idle", {})
    checks.append(_check("idle_baseline_measured", all(idle.get(s, {}).get("count", 0) > 0 for s in ("home", "report", "dispatch")), idle))
    minutes = evidence.get("duration_minutes", 0)
    windows = evidence.get("windows", [])
    checks.append(_check("one_measurement_window_per_minute", minutes > 0 and len(windows) >= max(1, minutes - 1),
                         {"windows": len(windows), "minutes": minutes}))
    ratio_checks = {"home": _latency_check("home_p95_within_threshold_of_idle", "home", evidence),
                    "report": _latency_check("report_p95_within_threshold_of_idle", "report", evidence),
                    "dispatch": _latency_check("dispatch_within_threshold_of_idle", "dispatch", evidence)}
    checks.extend(ratio_checks.values())
    flood_ok = sum(w.get("flood", {}).get("backend_requests_ok", 0) for w in windows)
    checks.append(_check("backends_answered_the_message_flood", flood_ok > 0, {"answered": flood_ok}))

    budgets = evidence.get("budgets", {})
    checks.append(_loads_check(evidence))
    start, end = evidence.get("load_started_ms", 0), evidence.get("load_finished_ms", 0)
    load = [s for s in samples if start <= s["at_ms"] <= end]
    checks.append(_check("host_samples_cover_the_load", len(load) >= max(1, min_samples), {"samples": len(load), "required": max(1, min_samples)}))
    peak_memory = max((s["backends"]["memory_bytes"] for s in load), default=0)
    # CPU is judged on the kernel's counters, averaged over each interval, when they exist: the first sample has no
    # previous reading and falls back to a one-second rate that can read a few percent over an enforced quota.
    cpu_samples = [s for s in load if s["backends"].get("cpu_source") == "cgroup"] or load
    peak_cpu = max((s["backends"]["cpu_millis"] for s in cpu_samples), default=0.0)
    checks.append(_check("backend_memory_within_budget", load and peak_memory <= budgets.get("memory_mib", 0) * 1024 * 1024,
                         {"peak_bytes": peak_memory, "budget_mib": budgets.get("memory_mib")}))
    checks.append(_check("backend_cpu_within_budget", load and peak_cpu <= budgets.get("cpu_millis", 0),
                         {"peak_millis": round(peak_cpu, 1), "budget_millis": budgets.get("cpu_millis")}))
    expected = budgets.get("running_backends", 0)
    crashes = [s["at_ms"] for s in load if s["backends"]["running"] != expected or s["backends"]["exited"] or s["backends"]["restarting"]
               or s["backends"]["oom_killed"] or s["backends"]["restart_count"]]
    checks.append(_check("no_backend_crash", load and not crashes, {"expected_running": expected, "bad_samples": crashes[:20]}))
    ratios = {series: check["detail"]["worst_ratio"] for series, check in ratio_checks.items()}
    passed = all(c["passed"] for c in checks)
    return {
        "passed": passed, "counts_as_ops08_evidence": passed and minutes >= OPS08_MINUTES, "checks": checks,
        "unit_level_probes": [p["name"] for p in probes if not p.get("native")],
        "worst_ratios": ratios, "peak_backend_memory_bytes": peak_memory, "peak_backend_cpu_millis": round(peak_cpu, 1),
    }


# ---- configuration ----

def soak_image(value):
    match = re.fullmatch(r"localhost:([0-9]{1,5})/portal-soak-backend@sha256:[a-f0-9]{64}", value or "")
    if not match or not 1 <= int(match.group(1)) <= 65535:
        raise RuntimeError("the soak backend image must be an owned loopback digest reference")
    return value


def duration_minutes(value):
    if not re.fullmatch(r"[0-9]+", value or "") or not 1 <= int(value) <= 180:
        raise RuntimeError("the duration is 1 to 180 minutes")
    return int(value)


def baseline_seconds(minutes):
    """A smoke measures its idle baseline briefly; the acceptance run measures it for three minutes."""
    return 30 if minutes < 30 else 180


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


def sampling_loop(sampler, stop, path):
    """Samples until stopped; a failed sample is recorded as an error row so a gap is never silent."""
    samples = []
    with open(path, "a", encoding="utf-8") as sink:
        while not stop.is_set():
            began = time.time()
            try:
                row = sampler.sample()
                samples.append(row)
            except Exception as error:  # noqa: BLE001 - the loop must outlive one bad sample
                row = {"at_ms": int(began * 1000), "error": str(error)[:500]}
            sink.write(json.dumps(row, sort_keys=True) + "\n")
            sink.flush()
            stop.wait(max(0.0, SAMPLE_INTERVAL_SECONDS - (time.time() - began)))
    return samples


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
    args = parser.parse_args(argv)
    minutes = duration_minutes(args.duration_minutes)
    image = soak_image(os.environ.get("APP_CAPACITY_SOAK_IMAGE"))
    for path in (args.portal_binary, args.journal_binary, args.out):
        if not Path(path).is_absolute():
            raise RuntimeError("paths must be absolute")
    require_disposable_environment(os.environ, os.geteuid(), sys.platform, shutil.which)
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    socket = os.path.realpath("/var/run/docker.sock")
    image_id = run_command("docker", "image", "inspect", image, "--format", "{{.Id}}").strip()
    if not re.fullmatch(r"sha256:[a-f0-9]{64}", image_id):
        raise RuntimeError("the soak image is not in the local Docker store")

    root = Path(tempfile.mkdtemp(prefix="portal-capacity-soak-", dir="/tmp"))
    root.chmod(0o755)
    volume = root / "owned.xfs"
    mountpoint = root / "mount"
    mountpoint.mkdir()
    loop = None
    parent_passed, evidence, samples, started = False, {}, [], time.time()
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
                        APP_CAPACITY_SOAK_MINUTES=str(minutes), APP_CAPACITY_SOAK_BASELINE_SECONDS=str(baseline_seconds(minutes)))
        soak_env.pop("APP_DATA_NATIVE_PHASE")
        sampler = Sampler(run_command, "/proc", mountpoint, image_id)
        stop = threading.Event()
        collected = []
        worker = threading.Thread(target=lambda: collected.extend(sampling_loop(sampler, stop, out / "samples.jsonl")), daemon=True)
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

    verdict = evaluate(evidence, samples, parent_passed, min_samples=minutes)
    for name in ("portal-parent.log", "containers.txt"):
        try:
            print(f"--- tail of {name} ---\n" + (out / name).read_text(errors="replace")[-6000:], file=sys.stderr)
        except OSError:
            pass
    report = {
        "format": EVIDENCE_FORMAT, "requested_minutes": minutes, "elapsed_seconds": int(time.time() - started),
        "ops08_minutes_required": OPS08_MINUTES,
        "run": {key: os.environ.get(key, "") for key in ("GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT", "GITHUB_SERVER_URL", "GITHUB_REPOSITORY", "GITHUB_SHA", "GITHUB_REF")},
        "host": {"kernel": os.uname().release, "architecture": os.uname().machine, "cpus": os.cpu_count(),
                 "volume_bytes": VOLUME_BYTES, "image": image, "image_id": image_id},
        "child": evidence, "samples": samples, "runner_log": read_json_lines(out / "runner-log.jsonl"), "verdict": verdict,
    }
    (out / "capacity-soak-evidence.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
    summary = {"passed": verdict["passed"], "counts_as_ops08_evidence": verdict["counts_as_ops08_evidence"],
               "failed_checks": [c["name"] for c in verdict["checks"] if not c["passed"]], "worst_ratios": verdict["worst_ratios"],
               "peak_backend_memory_bytes": verdict["peak_backend_memory_bytes"], "peak_backend_cpu_millis": verdict["peak_backend_cpu_millis"]}
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0 if verdict["passed"] else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
