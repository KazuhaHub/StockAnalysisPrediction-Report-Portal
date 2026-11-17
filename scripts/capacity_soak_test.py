import importlib.util
import json
import re
import tempfile
import unittest
from pathlib import Path

SOURCE = Path(__file__).parent / "acceptance/capacity_soak.py"
SPEC = importlib.util.spec_from_file_location("capacity_soak", SOURCE)
soak = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(soak)

MEMINFO = "MemTotal:       16380000 kB\nMemFree:         1000000 kB\nMemAvailable:   12000000 kB\nBuffers: 1 kB\n"
IMAGE = "localhost:5000/portal-soak-backend@sha256:" + "a" * 64


class ParserTests(unittest.TestCase):
    def test_meminfo(self):
        self.assertEqual(soak.parse_meminfo(MEMINFO), {"total_kib": 16380000, "available_kib": 12000000})
        with self.assertRaises(ValueError):
            soak.parse_meminfo("MemTotal: 1 kB\n")

    def test_proc_stat_cpu_and_percentage(self):
        first = soak.parse_proc_stat("cpu  100 0 100 700 100 0 0 0 0 0\ncpu0 1 1 1 1\n")
        second = soak.parse_proc_stat("cpu  200 0 200 1400 200 0 0 0 0 0\n")
        # busy = user+nice+system+irq+softirq+steal; idle = idle+iowait
        self.assertEqual(first, (200, 1000))
        self.assertEqual(second, (400, 2000))
        self.assertAlmostEqual(soak.cpu_percent(first, second), 20.0)
        self.assertEqual(soak.cpu_percent(first, first), 0.0)

    def test_loadavg_tasks(self):
        self.assertEqual(soak.parse_loadavg_tasks("0.50 0.40 0.30 3/512 12345\n"), (3, 512))
        with self.assertRaises(ValueError):
            soak.parse_loadavg_tasks("garbage")

    def test_docker_sizes(self):
        for text, want in [("0B", 0), ("512kB", 512_000), ("1KiB", 1024), ("123.5MiB", int(123.5 * 1024 ** 2)),
                           ("1.5GiB", int(1.5 * 1024 ** 3)), ("2MB", 2_000_000)]:
            self.assertEqual(soak.parse_docker_size(text), want, text)
        with self.assertRaises(ValueError):
            soak.parse_docker_size("12 parsecs")

    def test_docker_stats_row(self):
        row = json.dumps({"ID": "abc123", "MemUsage": "100MiB / 497MiB", "CPUPerc": "24.50%", "PIDs": "17"})
        self.assertEqual(soak.parse_docker_stats_row(row),
                         {"id": "abc123", "memory_bytes": 100 * 1024 ** 2, "cpu_millis": 245.0, "pids": 17})
        with self.assertRaises(ValueError):
            soak.parse_docker_stats_row(json.dumps({"ID": "x", "MemUsage": "bad", "CPUPerc": "1%", "PIDs": "1"}))

    def test_cgroup_cpu_usage(self):
        self.assertEqual(soak.parse_cgroup_usage_usec("usage_usec 123456\nuser_usec 1\n"), 123456)
        with self.assertRaises(ValueError):
            soak.parse_cgroup_usage_usec("user_usec 1\n")

    def test_cpu_millis_from_cgroup_delta(self):
        # 1.5 CPU seconds used over 6 wall seconds is a quarter of a core: 250 millis.
        self.assertAlmostEqual(soak.cpu_millis_between(10_000_000, 11_500_000, 6.0), 250.0)
        self.assertEqual(soak.cpu_millis_between(5, 5, 0.0), 0.0)


class SamplerTests(unittest.TestCase):
    def make(self, root, commands):
        calls = []

        def run(*args):
            calls.append(args)
            for prefix, answer in commands.items():
                if " ".join(args).startswith(prefix):
                    return answer
            raise AssertionError("unexpected command: " + " ".join(args))

        proc = Path(root) / "proc"
        proc.mkdir()
        (proc / "meminfo").write_text(MEMINFO)
        (proc / "stat").write_text("cpu  100 0 100 700 100 0 0 0 0 0\n")
        (proc / "loadavg").write_text("0.5 0.4 0.3 4/600 99\n")
        mount = Path(root) / "mount"
        mount.mkdir()
        return soak.Sampler(run=run, proc=proc, mountpoint=mount, image_id="sha256:" + "b" * 64, clock=lambda: 1000.0), calls

    def test_sample_without_backends(self):
        with tempfile.TemporaryDirectory() as root:
            sampler, _ = self.make(root, {"docker ps": ""})
            sample = sampler.sample()
        self.assertEqual(sample["mem_total_kib"], 16380000)
        self.assertEqual(sample["tasks_total"], 600)
        self.assertEqual(sample["backends"]["running"], 0)
        self.assertEqual(sample["backends"]["memory_bytes"], 0)
        self.assertGreater(sample["xfs_total_bytes"], 0)
        self.assertEqual(sample["at_ms"], 1_000_000)

    def test_sample_sums_the_backends_and_flags_a_dead_one(self):
        log = None
        with tempfile.TemporaryDirectory() as root:
            log = Path(root) / "container-json.log"
            log.write_bytes(b"x" * 1000)
            (Path(root) / "container-json.log.1").write_bytes(b"y" * 500)
            states = {
                "aaa": {"Running": True, "Restarting": False, "OOMKilled": False, "Status": "running"},
                "bbb": {"Running": False, "Restarting": False, "OOMKilled": True, "Status": "exited"},
            }

            def inspect(args):
                container = args[-1]
                return json.dumps({"State": states[container], "LogPath": str(log), "RestartCount": 0})

            def stats(*args):
                rows = [{"ID": "aaa", "MemUsage": "200MiB / 497MiB", "CPUPerc": "24%", "PIDs": "40"}]
                return "\n".join(json.dumps(r) for r in rows)

            sampler, _ = self.make(root, {})
            sampler.run = lambda *args: (
                "aaa\nbbb\n" if args[:2] == ("docker", "ps") else
                inspect(args) if args[:2] == ("docker", "inspect") else
                stats(*args) if args[:2] == ("docker", "stats") else (_ for _ in ()).throw(AssertionError(args)))
            sample = sampler.sample()
        backends = sample["backends"]
        self.assertEqual((backends["total"], backends["running"], backends["oom_killed"]), (2, 1, 1))
        self.assertEqual(backends["memory_bytes"], 200 * 1024 ** 2)
        self.assertAlmostEqual(backends["cpu_millis"], 240.0)
        self.assertEqual(backends["pids"], 40)
        self.assertEqual(sample["docker_log_bytes"], 2 * 1500)



class CgroupSampleTests(unittest.TestCase):
    def test_sample_records_each_backends_own_cgroup_counters(self):
        full = "f" * 64
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            scope = root / "cg" / f"docker-{full}.scope"
            scope.mkdir(parents=True)
            for name, text in {"cpu.stat": "usage_usec 5000000\nuser_usec 1\n", "memory.current": "400000000\n", "memory.max": "521142272\n",
                               "cpu.max": "24600 100000\n", "pids.current": "58\n", "pids.max": "64\n", "memory.peak": "410000000\n",
                               "pids.peak": "60\n"}.items():
                (scope / name).write_text(text)
            proc = root / "proc"
            proc.mkdir()
            (proc / "meminfo").write_text(MEMINFO)
            (proc / "stat").write_text("cpu  100 0 100 700 100 0 0 0 0 0\n")
            (proc / "loadavg").write_text("0.5 0.4 0.3 4/600 99\n")
            (root / "mount").mkdir()

            def run(*args):
                if args[:2] == ("docker", "ps"):
                    return "aaa\n"
                if args[:2] == ("docker", "inspect"):
                    return json.dumps({"Id": full, "State": {"Running": True}, "RestartCount": 0})
                return json.dumps({"ID": "aaa", "MemUsage": "400MiB / 497MiB", "CPUPerc": "20.00%", "PIDs": "58"})

            sampler = soak.Sampler(run=run, proc=proc, mountpoint=root / "mount", image_id="sha256:" + "b" * 64, clock=lambda: 1000.0,
                                   cgroup_root=root / "cg")
            rows = sampler.sample()["backends"]["per_backend"]
        self.assertEqual(rows, [{"id": "aaa", "usage_usec": 5000000, "memory_current": 400000000, "memory_max": 521142272,
                                 "cpu_quota_millis": 246.0, "pids_current": 58, "pids_max": 64, "memory_peak": 410000000, "pids_peak": 60}])

    def test_cgroup_limit_and_cpu_max_parsers(self):
        self.assertEqual(soak.parse_cgroup_number("64\n"), 64)
        self.assertIsNone(soak.parse_cgroup_number("max\n"))
        self.assertEqual(soak.parse_cpu_max_millis("24600 100000\n"), 246.0)
        self.assertEqual(soak.parse_cpu_max_millis("200000 100000"), 2000.0)
        self.assertIsNone(soak.parse_cpu_max_millis("max 100000"))

    def test_a_failed_host_sample_is_recorded_as_an_error_row_and_counted(self):
        class Flaky:
            calls = 0

            def sample(self):
                Flaky.calls += 1
                if Flaky.calls == 2:
                    raise RuntimeError("docker stats timed out")
                return {"at_ms": Flaky.calls}

        import threading
        stop = threading.Event()
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "samples.jsonl"
            original = Flaky.sample

            def sample_then_stop(self):
                try:
                    return original(self)
                finally:
                    if Flaky.calls >= 3:
                        stop.set()

            Flaky.sample = sample_then_stop
            samples, failed = soak.sampling_loop(Flaky(), stop, path, interval=0)
            rows = [json.loads(line) for line in path.read_text().splitlines()]
        self.assertEqual((len(samples), failed), (2, 1))
        self.assertEqual([("error" in r) for r in rows], [False, True, False])


LIMIT_MIB = 497
PERIOD_MS = 60_000
LOAD_START_MS = 1_000_000


def backend_status(index, window=1, **changes):
    status = {"installation": f"installation-{index}", "answered": True, "held_bytes": int(0.85 * LIMIT_MIB) * 1024 ** 2, "threads": 40,
              "burners": 1, "log_bytes": 900000 + window * 60_000_000, "storage_bytes": 256 * 1024 ** 2, "storage_full": 2,
              "data_present": True, "requests": 10 + window * 40}
    status.update(changes)
    return status


def stats(p95, count=100):
    return {"count": count, "p50_us": 1, "p95_us": p95, "max_us": 5}


def flood(answered=600, busy=7000, errors=0, alive=True, heartbeat_age_ms=1500, classes=None):
    return {"backend_requests": answered + busy + errors, "backend_requests_ok": answered, "backend_requests_busy": busy,
            "backend_request_errors": errors, "flood_alive": alive, "heartbeat_age_ms": heartbeat_age_ms,
            "classes": classes if classes is not None else {"200": answered, "429:app_platform_busy": busy}}


TASK_WINDOW_CREATED = 66


def task_stats(count=66, p95=800, maximum=2500):
    return {"count": count, "p50_ms": 200, "p95_ms": p95, "max_ms": maximum}


def window_tasks(**changes):
    value = {"created": TASK_WINDOW_CREATED, "created_backend": 36, "created_event": 30, "succeeded": TASK_WINDOW_CREATED, "failed": 0,
             "uncertain": 0, "review_required": 0, "cancelled": 0, "open": 3, "oldest_open_age_ms": 1500, "latency_ms": task_stats(),
             "worker_alive": True}
    value.update(changes)
    return value


def window_events(**changes):
    value = {"captured": 5, "capture_errors": 0, "delivered": 30, "max_inflight_per_subscription": 3, "subscriptions_active": 6, "subscriptions_fenced": 0}
    value.update(changes)
    return value


def window_quota(**changes):
    value = {"max_installation_active": soak.TASK_INSTALLATION_QUOTA, "saturated_active": {"sat-0": soak.TASK_INSTALLATION_QUOTA, "sat-1": soak.TASK_INSTALLATION_QUOTA}}
    value.update(changes)
    return value


def window_backend_flood(requests=2400, answered=120, **changes):
    value = {"requests": requests, "answered": answered, "refused": requests - answered, "alive": True, "heartbeat_age_ms": 900,
             "classes": {"200": answered, "429:task_capacity": requests - answered},
             "forged_requests": 640, "forged_classes": {"404:app_platform_task_unavailable": 640}}
    value.update(changes)
    return value


def window_workload_submit(**changes):
    value = {"requests": 36, "answered": 36, "classes": {"200": 36}, "latency_ms": {"count": 36, "p50_ms": 25, "p95_ms": 90, "max_ms": 240}}
    value.update(changes)
    return value


def platform_work(windows):
    subscriptions = [{"installation": f"w-{i}", "role": "workload", "state": "active", "reason": ""} for i in range(6)]
    subscriptions += [{"installation": "sat-1", "role": "saturated", "state": "review_required", "reason": "task_capacity"},
                      {"installation": "sat-0", "role": "probe", "state": "review_required", "reason": "inflight_limit"}]
    total = TASK_WINDOW_CREATED * windows
    return {"workload_tenants": 6, "saturated_tenants": 2, "worker_interval_ms": 1000, "loaded_latency_ms": task_stats(count=total),
            "tasks_total": {"created": total, "admit_errors": 0, "by_state": {"succeeded": total}},
            "events_total": {"captured": 5 * windows, "expected_deliveries": 30 * windows, "delivered": 30 * windows, "capture_errors": 0},
            "drain": {"seconds": 4, "open_after": 0, "timed_out": False}, "subscriptions": subscriptions, "saturated_tasks_run": 0}


def child_evidence(windows=3, home=1000, report=2000, dispatch=300, batch=400, flood_ok=100):
    def window(m, scale):
        return {
            "minute": m, "at_ms": LOAD_START_MS + PERIOD_MS * m, "started_ms": LOAD_START_MS + PERIOD_MS * (m - 1),
            "ended_ms": LOAD_START_MS + PERIOD_MS * m,
            "home": stats(int(home * scale)), "report": stats(int(report * scale)), "dispatch": stats(int(dispatch * scale)),
            "batch_start": stats(int(batch * scale), 20),
            "flood": flood(answered=flood_ok),
            "backends": [backend_status(i, window=m) for i in range(8)],
            "tasks": window_tasks(), "events": window_events(), "quota": window_quota(), "backend_flood": window_backend_flood(),
            "workload_submit": window_workload_submit(),
        }
    return {
        "format": "portal.capacity-soak.child/1-dev.1",
        "budgets": {"memory_mib": 3993, "cpu_millis": 2000, "disk_bytes": 8 << 30, "image_store_bytes": 10 << 30, "running_backends": 8},
        "sizing": {"MemoryMiB": LIMIT_MIB, "CPUMillis": 246},
        "probes": [{"name": name, "expected": "x", "observed": "x", "passed": True, "level": "native",
                    **({"retired": "managed_uninstall"} if name in soak.RETIRED_PROBES else {})} for name in soak.REQUIRED_PROBES]
                  + [{"name": "task_over_application_quota", "expected": "task_capacity", "observed": None, "passed": None, "level": "unit",
                      "tests": ["TestTaskQuotasRefuseAtTheirOwnBoundary"]}],
        "idle": {**window(0, 1.0)},
        "loaded": {series: {"count": 100 * windows, "p50_us": 1, "p95_us": int(base * 1.2), "max_us": 9}
                   for series, base in (("home", home), ("report", report), ("dispatch", dispatch), ("dispatch_shallow", dispatch), ("batch_start", batch))},
        "windows": [window(m, 1.2) for m in range(1, windows + 1)],
        "load_started_ms": LOAD_START_MS, "load_finished_ms": LOAD_START_MS + PERIOD_MS * windows, "duration_minutes": windows,
        "session_expires_ms": LOAD_START_MS + PERIOD_MS * windows + 3_600_000,
        "db_driver": "postgres", "db_version": "16.4",
        "markers": ["load_finished"],
        "platform_work": platform_work(windows),
    }


CPU_QUOTA_MILLIS = 246
PIDS_MAX = 64


def per_backend(index, at_ms, usage_usec, mem_fraction=0.9, pids=58, quota=CPU_QUOTA_MILLIS):
    return {"id": f"container-{index}", "usage_usec": usage_usec, "memory_current": int(mem_fraction * LIMIT_MIB * 1024 ** 2),
            "memory_max": LIMIT_MIB * 1024 ** 2, "cpu_quota_millis": quota, "pids_current": pids, "pids_max": PIDS_MAX}


def journal_reading(at_ms, rate=100):
    """The runner's journal files as the sampler records them: a database that grows by `rate` bytes a second from the start of the load."""
    seconds = max((at_ms - LOAD_START_MS) // 1000, 0)
    database, wal = 4 * 1024 ** 2 + seconds * rate, 1024 ** 2
    return {"files": {"journal.sqlite3": database, "journal.sqlite3-wal": wal}, "total": database + wal}


def backend_sample(at_ms, mem_mib=3900, cpu=1900.0, running=8, total=8, oom=0, restarts=0, usage_fraction=0.95, mem_fraction=0.9, pids=58,
                   available_kib=12_000_000, detail=True, journal_rate=100):
    """One host sample. usage_usec counts from LOAD_START_MS so a backend at usage_fraction burns that share of its quota."""
    seconds = (at_ms - LOAD_START_MS) / 1000.0
    usage = int(max(seconds, 0) * CPU_QUOTA_MILLIS * usage_fraction * 1000.0)
    sample = {"at_ms": at_ms, "mem_total_kib": 16_380_000, "mem_available_kib": available_kib,
              "backends": {"total": total, "running": running, "oom_killed": oom, "restarting": 0, "exited": total - running,
                           "restart_count": restarts, "memory_bytes": mem_mib * 1024 ** 2, "cpu_millis": cpu, "pids": 400}}
    if detail:
        sample["backends"]["per_backend"] = [per_backend(i, at_ms, usage, mem_fraction=mem_fraction, pids=pids) for i in range(8)]
    if journal_rate is not None:
        sample["journal_bytes"] = journal_reading(at_ms, journal_rate)
    return sample


def load_samples(windows, step_ms=30_000, **changes):
    """Samples every step from one step before the load to its end: enough pairs for every window."""
    end = LOAD_START_MS + PERIOD_MS * windows
    return [backend_sample(at, **changes) for at in range(LOAD_START_MS - step_ms, end + 1, step_ms)]


class EvaluateTests(unittest.TestCase):
    def verdict(self, evidence=None, samples=None, minutes=3, exit_ok=True, **kwargs):
        evidence = evidence or child_evidence(minutes)
        samples = samples if samples is not None else load_samples(minutes)
        kwargs.setdefault("unit_tests", {"TestTaskQuotasRefuseAtTheirOwnBoundary": True})
        kwargs.setdefault("event", "workflow_dispatch")
        return soak.evaluate(evidence, samples, parent_passed=exit_ok, **kwargs)

    def failing(self, verdict):
        return {c["name"] for c in verdict["checks"] if not c["passed"]}

    def check(self, verdict, name):
        return next(c for c in verdict["checks"] if c["name"] == name)

    def test_a_clean_run_passes(self):
        verdict = self.verdict()
        self.assertTrue(verdict["passed"], verdict)
        self.assertEqual(self.failing(verdict), set())

    def test_the_relative_threshold_is_the_larger_of_one_and_a_half_times_and_five_milliseconds_more(self):
        self.assertEqual(soak.LATENCY_FLOOR_US, 5000)
        self.assertEqual(soak.latency_threshold_us(1000), 6000)      # the floor governs a sub-millisecond baseline
        self.assertEqual(soak.latency_threshold_us(10000), 15000)    # the two rules meet at 10 ms
        self.assertEqual(soak.latency_threshold_us(40000), 60000)    # the ratio governs a slow baseline
        self.assertEqual(soak.latency_threshold_us(0), 5000)         # an unmeasured baseline is not a free pass

    def test_the_absolute_bounds_are_the_owner_decision_of_2026_11_15(self):
        # Under full declared load on the reference host: home and report pooled p95 at most 50 ms, batch start at most 100 ms.
        self.assertEqual(soak.HOME_P95_BOUND_US, 50_000)
        self.assertEqual(soak.REPORT_P95_BOUND_US, 50_000)
        self.assertEqual(soak.BATCH_START_P95_BOUND_US, 100_000)
        self.assertEqual(soak.DISPATCH_P95_BOUND_US, 25_000)      # owner decision 2026-11-15, after the relative floor was not reliably met
        self.assertEqual(soak.ABSOLUTE_P95_BOUND_US, {"home": 50_000, "report": 50_000, "dispatch": 25_000, "batch_start": 100_000})

    def test_home_report_and_batch_start_pass_at_their_absolute_bound_and_fail_one_microsecond_beyond(self):
        for series, bound in (("home", 50_000), ("report", 50_000), ("dispatch", 25_000), ("batch_start", 100_000)):
            name = f"{series}_p95_within_absolute_bound"
            evidence = child_evidence()
            evidence["loaded"][series]["p95_us"] = bound
            self.assertTrue(self.verdict(evidence)["passed"], series)
            evidence["loaded"][series]["p95_us"] = bound + 1
            self.assertEqual(self.failing(self.verdict(evidence)), {name}, series)

    def test_a_pooled_p95_far_over_the_old_relative_threshold_passes_when_it_is_inside_the_absolute_bound(self):
        evidence = child_evidence(home=1000)
        evidence["loaded"]["home"]["p95_us"] = 13_000   # 13 times idle: the relative gate would fail
        verdict = self.verdict(evidence)
        self.assertTrue(verdict["passed"], self.failing(verdict))
        detail = self.check(verdict, "home_p95_within_absolute_bound")["detail"]
        # The relative threshold and the ratio to idle stay recorded as diagnostics.
        self.assertEqual(detail["bound_us"], 50_000)
        self.assertEqual(detail["relative_threshold_us"], 6000)
        self.assertFalse(detail["within_relative_threshold"])
        self.assertEqual(detail["pooled_ratio"], 13.0)
        self.assertEqual(detail["threshold_us"], 50_000)

    def test_dispatch_is_judged_on_its_absolute_bound_and_keeps_the_relative_threshold_as_a_diagnostic(self):
        evidence = child_evidence()
        idle = evidence["idle"]["dispatch"]["p95_us"]
        evidence["loaded"]["dispatch"]["p95_us"] = soak.latency_threshold_us(idle) + 4000   # over the old relative gate, far inside 25 ms
        verdict = self.verdict(evidence)
        self.assertTrue(verdict["passed"], self.failing(verdict))
        detail = self.check(verdict, "dispatch_p95_within_absolute_bound")["detail"]
        self.assertEqual((detail["bound_us"], detail["bound_kind"], detail["within_relative_threshold"]), (25_000, "absolute", False))
        self.assertEqual(detail["relative_threshold_us"], soak.latency_threshold_us(idle))

    def test_the_shallow_dispatch_measurement_is_recorded_and_never_judged(self):
        evidence = child_evidence()
        evidence["loaded"]["dispatch_shallow"]["p95_us"] = 900_000
        for window in evidence["windows"]:
            window["dispatch_shallow"] = stats(900_000)
        verdict = self.verdict(evidence)
        self.assertTrue(verdict["passed"], self.failing(verdict))
        self.assertIsNotNone(verdict["informational_shallow_dispatch_worst_ratio"])

    def test_a_pooled_pass_still_lists_the_windows_over_the_bound_as_diagnostics(self):
        evidence = child_evidence(windows=4)
        evidence["windows"][1]["home"]["p95_us"] = 50_001
        evidence["windows"][3]["home"]["p95_us"] = 90_000
        verdict = self.verdict(evidence, minutes=4)
        self.assertTrue(verdict["passed"], verdict)
        detail = self.check(verdict, "home_p95_within_absolute_bound")["detail"]
        self.assertEqual(detail["windows_over_the_threshold"], [2, 4])
        self.assertEqual(detail["windows_over_count"], 2)
        self.assertEqual(detail["worst_window"], {"minute": 4, "p95_us": 90_000})
        self.assertEqual(len(detail["window_thresholds"]), 4)
        self.assertEqual(detail["pooled_p95_us"], evidence["loaded"]["home"]["p95_us"])

    def test_no_pooled_loaded_sample_is_not_a_pass(self):
        for loaded in ({}, {"home": {"count": 0, "p95_us": 0}}):
            evidence = child_evidence()
            evidence["loaded"] = loaded
            self.assertIn("home_p95_within_absolute_bound", self.failing(self.verdict(evidence)))

    def test_the_step_summary_lists_every_window_of_every_series_and_names_the_bound(self):
        evidence = child_evidence(windows=3)
        verdict = self.verdict(evidence, minutes=3)
        text = soak.render_summary(evidence, verdict)
        for series in ("home", "report", "dispatch", "batch_start"):
            self.assertIn(f"### {series}", text)
        self.assertEqual(text.count("| 1 |"), 4)  # minute 1 appears once in each series table
        self.assertEqual(text.count("| 3 |"), 4)
        self.assertIn("pooled", text)
        self.assertIn("50000 us", text)

    def test_the_effective_bound_and_the_relative_threshold_of_every_window_are_recorded(self):
        verdict = self.verdict()
        rows = self.check(verdict, "home_p95_within_absolute_bound")["detail"]["window_thresholds"]
        self.assertEqual(len(rows), 3)
        self.assertEqual(rows[0], {"minute": 1, "p95_us": 1200, "threshold_us": 50_000, "relative_threshold_us": 6000, "passed": True})

    def test_summed_backend_memory_and_cpu_must_stay_inside_the_budgets(self):
        samples = load_samples(3)
        over_memory = [dict(s) for s in samples]
        over_memory[2] = backend_sample(samples[2]["at_ms"], mem_mib=3994)
        self.assertEqual(self.failing(self.verdict(samples=over_memory)), {"backend_memory_within_budget"})
        over_cpu = [dict(s) for s in samples]
        over_cpu[2] = backend_sample(samples[2]["at_ms"], cpu=2000.5)
        self.assertEqual(self.failing(self.verdict(samples=over_cpu)), {"backend_cpu_within_budget"})
        exact = [dict(s) for s in samples]
        exact[2] = backend_sample(samples[2]["at_ms"], mem_mib=3993, cpu=2000.0)
        self.assertTrue(self.verdict(samples=exact)["passed"])

    def test_samples_outside_the_load_do_not_count(self):
        before_load = [backend_sample(100, mem_mib=9999, cpu=9999.0)] + load_samples(3)
        self.assertTrue(self.verdict(samples=before_load)["passed"])

    def test_a_missing_exited_oom_or_restarted_backend_is_a_crash(self):
        for changes in ({"running": 7}, {"oom": 1}, {"restarts": 1}):
            samples = load_samples(3)
            samples[3] = backend_sample(samples[3]["at_ms"], **changes)
            self.assertIn("no_backend_crash", self.failing(self.verdict(samples=samples)), changes)

    def test_no_samples_during_the_load_is_a_failure_not_a_pass(self):
        self.assertIn("host_samples_cover_the_load", self.failing(self.verdict(samples=[])))

    def test_at_least_ninety_percent_of_the_expected_host_samples_must_exist_and_failed_ones_are_counted(self):
        # Three minutes of load at a thirty-second interval expect six samples; ninety percent of that is six (rounded up).
        samples = [s for s in load_samples(3) if LOAD_START_MS < s["at_ms"] <= LOAD_START_MS + 3 * PERIOD_MS]
        self.assertEqual(len(samples), 6)
        verdict = self.verdict(samples=load_samples(3))
        detail = self.check(verdict, "host_samples_cover_the_load")["detail"]
        self.assertEqual((detail["expected"], detail["required"]), (6, 6))
        short = [s for s in load_samples(3) if s["at_ms"] != LOAD_START_MS + 2 * PERIOD_MS]
        self.assertIn("host_samples_cover_the_load", self.failing(self.verdict(samples=short)))
        failed = self.verdict(samples=load_samples(3), failed_samples=1)
        self.assertIn("host_samples_cover_the_load", self.failing(failed))
        self.assertEqual(self.check(failed, "host_samples_cover_the_load")["detail"]["failed_samples"], 1)

    def test_host_free_memory_is_judged_and_recorded(self):
        samples = load_samples(3)
        samples[4] = backend_sample(samples[4]["at_ms"], available_kib=1_500_000)  # 9.2% of 16.38 GB
        verdict = self.verdict(samples=samples)
        self.assertEqual(self.failing(verdict), {"host_memory_headroom"})
        self.assertEqual(self.check(verdict, "host_memory_headroom")["detail"]["lowest_available_kib"], 1_500_000)
        self.assertEqual(self.check(self.verdict(), "host_memory_headroom")["detail"]["lowest_available_kib"], 12_000_000)

    def test_a_required_probe_that_never_ran_fails(self):
        evidence = child_evidence()
        evidence["probes"] = [p for p in evidence["probes"] if p["name"] != "ninth_backend"]
        self.assertIn("every_required_probe_ran", self.failing(self.verdict(evidence)))

    def test_every_backend_probe_must_be_retired_through_the_managed_uninstall(self):
        for retired in ("refused", "workaround", None):
            evidence = child_evidence()
            probe = next(p for p in evidence["probes"] if p["name"] == "memory_over_budget")
            if retired is None:
                del probe["retired"]
            else:
                probe["retired"] = retired
            self.assertEqual(self.failing(self.verdict(evidence)), {"probes_retired_through_the_managed_uninstall"}, retired)

    def test_an_unrefused_native_probe_fails(self):
        evidence = child_evidence()
        evidence["probes"][0].update(observed="capacity_exhausted_disk", passed=False)
        verdict = self.verdict(evidence)
        self.assertIn("every_excess_request_refused_with_its_typed_error", self.failing(verdict))

    def test_unit_level_probes_are_reported_separately_with_no_observation_and_judged_on_their_tests(self):
        verdict = self.verdict()
        self.assertEqual(verdict["unit_level_probes"], [{"name": "task_over_application_quota", "tests": ["TestTaskQuotasRefuseAtTheirOwnBoundary"],
                                                          "tests_passed": True}])
        # A unit probe has no observation, and its absence does not fail the native refusal check.
        self.assertNotIn("every_excess_request_refused_with_its_typed_error", self.failing(verdict))
        # Its test must have been run in this job and passed.
        for unit_tests in ({}, {"TestTaskQuotasRefuseAtTheirOwnBoundary": False}, None):
            kwargs = {} if unit_tests is None else {"unit_tests": unit_tests}
            evidence = child_evidence()
            verdict = soak.evaluate(evidence, load_samples(3), parent_passed=True, **kwargs)
            self.assertIn("unit_level_probe_tests_passed", self.failing(verdict), unit_tests)

    def test_a_unit_probe_may_not_claim_an_observation_or_come_without_tests(self):
        evidence = child_evidence()
        evidence["probes"][-1]["observed"] = "task_capacity"
        self.assertIn("unit_level_probe_tests_passed", self.failing(self.verdict(evidence)))
        evidence = child_evidence()
        evidence["probes"][-1]["tests"] = []
        self.assertIn("unit_level_probe_tests_passed", self.failing(self.verdict(evidence)))

    def test_a_parent_failure_or_missing_load_marker_fails(self):
        self.assertIn("no_runner_crash", self.failing(self.verdict(exit_ok=False)))
        evidence = child_evidence()
        evidence["markers"] = []
        self.assertIn("load_completed", self.failing(self.verdict(evidence)))

    def test_empty_or_null_evidence_fails_instead_of_raising(self):
        for evidence in ({}, {"probes": None, "windows": None, "markers": None, "idle": None}):
            verdict = soak.evaluate(evidence, [], parent_passed=False)
            self.assertFalse(verdict["passed"])

    def test_too_few_windows_fail(self):
        evidence = child_evidence(windows=1)
        evidence["duration_minutes"] = 10
        self.assertIn("one_measurement_window_per_minute", self.failing(self.verdict(evidence)))

    def test_the_session_must_outlive_the_run(self):
        evidence = child_evidence()
        evidence["session_expires_ms"] = evidence["load_finished_ms"] - 1
        self.assertEqual(self.failing(self.verdict(evidence)), {"session_outlives_the_run"})
        del evidence["session_expires_ms"]
        self.assertIn("session_outlives_the_run", self.failing(self.verdict(evidence)))

    def test_a_flood_that_dies_after_the_first_minute_fails_even_though_it_answered_once(self):
        evidence = child_evidence(windows=4)
        for window in evidence["windows"][1:]:
            window["flood"] = flood(answered=0, busy=0, alive=False, heartbeat_age_ms=600_000, classes={})
        failed = self.failing(self.verdict(evidence, minutes=4))
        self.assertIn("message_flood_answered_in_every_window", failed)
        self.assertIn("message_flood_alive_throughout", failed)

    def test_every_window_needs_the_minimum_of_answered_requests(self):
        evidence = child_evidence()
        evidence["windows"][1]["flood"] = flood(answered=soak.FLOOD_MIN_ANSWERED_PER_WINDOW - 1)
        verdict = self.verdict(evidence)
        self.assertEqual(self.failing(verdict), {"message_flood_answered_in_every_window"})
        detail = self.check(verdict, "message_flood_answered_in_every_window")["detail"]
        self.assertEqual(detail["windows_below_the_minimum"], [2])
        evidence["windows"][1]["flood"] = flood(answered=soak.FLOOD_MIN_ANSWERED_PER_WINDOW)
        self.assertTrue(self.verdict(evidence)["passed"])

    def test_busy_refusals_are_typed_and_any_server_error_or_untyped_failure_fails(self):
        evidence = child_evidence(flood_ok=600)
        self.assertTrue(self.verdict(evidence)["passed"])
        detail = self.check(self.verdict(evidence), "message_flood_answered_in_every_window")["detail"]
        self.assertEqual(detail["answered"], 1800)
        self.assertEqual(detail["busy_refusals"], 21000)
        for classes in ({"200": 600, "429:app_platform_busy": 7000, "502:app_platform_unavailable": 3},
                        {"200": 600, "transport": 2}, {"200": 600, "429:": 4}, {"200": 600, "500:": 1}):
            evidence = child_evidence()
            evidence["windows"][2]["flood"] = flood(classes=classes)
            verdict = self.verdict(evidence)
            self.assertIn("message_flood_refusals_are_typed", self.failing(verdict), classes)
            self.assertEqual(self.check(verdict, "message_flood_refusals_are_typed")["detail"]["classes_by_window"][2], classes)

    def test_a_late_heartbeat_or_a_dead_flood_process_fails_the_liveness_check(self):
        for change in ({"alive": False}, {"heartbeat_age_ms": soak.FLOOD_HEARTBEAT_MAX_AGE_MS + 1}):
            evidence = child_evidence()
            evidence["windows"][2]["flood"] = flood(**change)
            self.assertEqual(self.failing(self.verdict(evidence)), {"message_flood_alive_throughout"}, change)

    def test_every_backend_must_show_it_ran_each_of_its_loads(self):
        for change in ({"answered": False}, {"held_bytes": 100 * 1024 ** 2}, {"threads": 3}, {"burners": 0}, {"log_bytes": 0},
                       {"storage_full": 0}, {"data_present": False}):
            evidence = child_evidence()
            for index, window in enumerate(evidence["windows"], start=1):
                window["backends"][3] = backend_status(3, window=index, **change)
            self.assertIn("backends_ran_every_declared_load", self.failing(self.verdict(evidence)), change)
        evidence = child_evidence()
        for window in evidence["windows"]:
            window["backends"] = window["backends"][:7]
        self.assertIn("backends_ran_every_declared_load", self.failing(self.verdict(evidence)))
        evidence = child_evidence()
        for window in evidence["windows"]:
            del window["backends"]
        self.assertIn("backends_ran_every_declared_load", self.failing(self.verdict(evidence)))

    def test_a_held_memory_below_the_held_fraction_of_the_limit_fails(self):
        self.assertEqual(soak.MEMORY_HELD_FRACTION, 0.85)
        evidence = child_evidence()
        for index, window in enumerate(evidence["windows"], start=1):
            window["backends"][1] = backend_status(1, window=index, held_bytes=int(0.80 * LIMIT_MIB) * 1024 ** 2)
        self.assertIn("backends_ran_every_declared_load", self.failing(self.verdict(evidence)))

    def test_status_counters_must_advance_in_every_window(self):
        for counter in ("log_bytes", "requests"):
            evidence = child_evidence(windows=4)
            frozen = backend_status(2, window=2)[counter]
            for window in evidence["windows"][2:]:
                window["backends"][2][counter] = frozen
            verdict = self.verdict(evidence, minutes=4)
            self.assertIn("backends_ran_every_declared_load", self.failing(verdict), counter)
            problems = self.check(verdict, "backends_ran_every_declared_load")["detail"]["problems"]
            self.assertTrue(any(counter in reason for p in problems for reason in p["reasons"]), problems)

    def test_a_backend_that_missed_a_status_request_is_judged_across_the_gap_but_missing_more_than_a_few_windows_fails(self):
        evidence = child_evidence(windows=60)
        evidence["windows"][10]["backends"][2] = {"installation": "installation-2", "answered": False}
        verdict = self.verdict(evidence, minutes=60, samples=load_samples(60))
        self.assertTrue(verdict["passed"], self.failing(verdict))
        for window in evidence["windows"][20:30]:
            window["backends"][2] = {"installation": "installation-2", "answered": False}
        self.assertIn("backends_ran_every_declared_load", self.failing(self.verdict(evidence, minutes=60, samples=load_samples(60))))

    def test_cpu_is_judged_on_cgroup_samples_when_there_are_any(self):
        samples = load_samples(3)
        for sample in samples:
            sample["backends"]["cpu_source"] = "cgroup"
        rate_only = backend_sample(LOAD_START_MS + 100, cpu=2020.0)
        rate_only["backends"]["cpu_source"] = "docker_stats"
        self.assertTrue(self.verdict(samples=samples + [rate_only])["passed"])
        self.assertIn("backend_cpu_within_budget", self.failing(self.verdict(samples=load_samples(3, cpu=2020.0))))

    def test_every_backend_must_use_most_of_its_cpu_quota_in_every_window(self):
        self.assertEqual(soak.CPU_QUOTA_USED_FRACTION, 0.70)
        # 60 percent of the quota is idle by this rule; the same backend at 75 percent passes.
        slow = [backend_sample(at, usage_fraction=0.60) for at in range(LOAD_START_MS - 30_000, LOAD_START_MS + 3 * PERIOD_MS + 1, 30_000)]
        verdict = self.verdict(samples=slow)
        self.assertEqual(self.failing(verdict), {"backends_used_their_cpu_quota_in_every_window"})
        ok = [backend_sample(at, usage_fraction=0.75) for at in range(LOAD_START_MS - 30_000, LOAD_START_MS + 3 * PERIOD_MS + 1, 30_000)]
        self.assertTrue(self.verdict(samples=ok)["passed"])

    def test_one_idle_backend_in_one_window_is_reported_with_its_window_and_fails(self):
        samples = load_samples(3)
        for sample in samples:
            if LOAD_START_MS + PERIOD_MS < sample["at_ms"] <= LOAD_START_MS + 2 * PERIOD_MS:
                # Window 2: backend 5 burns nothing (its usage stays what it was at the window's start).
                row = sample["backends"]["per_backend"][5]
                row["usage_usec"] = int((PERIOD_MS / 1000.0) * CPU_QUOTA_MILLIS * 0.95 * 1000.0) * 1
        verdict = self.verdict(samples=samples)
        self.assertIn("backends_used_their_cpu_quota_in_every_window", self.failing(verdict))
        problems = self.check(verdict, "backends_used_their_cpu_quota_in_every_window")["detail"]["problems"]
        self.assertTrue(any(p["window"] == 2 and p["backend"] == "container-5" for p in problems), problems)

    def test_memory_current_includes_the_page_cache_so_it_is_a_diagnostic_and_the_backends_own_held_bytes_decide(self):
        # A backend whose cgroup memory.current reads 50% of its limit (page cache dropped) but which holds its 85% itself still passes.
        samples = load_samples(3)
        samples[3]["backends"]["per_backend"][0]["memory_current"] = int(0.50 * LIMIT_MIB * 1024 ** 2)
        verdict = self.verdict(samples=samples)
        self.assertTrue(verdict["passed"], self.failing(verdict))
        self.assertAlmostEqual(verdict["achieved_fractions"]["memory_current_min"]["per_backend"]["container-0"], 0.50, places=3)

    def test_held_bytes_below_the_held_fraction_in_one_window_fail_even_when_the_last_window_is_fine(self):
        evidence = child_evidence(windows=4)
        evidence["windows"][1]["backends"][4] = backend_status(4, window=2, held_bytes=int(0.60 * LIMIT_MIB) * 1024 ** 2)
        verdict = self.verdict(evidence, minutes=4)
        self.assertEqual(self.failing(verdict), {"backends_ran_every_declared_load"})
        problems = self.check(verdict, "backends_ran_every_declared_load")["detail"]["problems"]
        self.assertTrue(any("window 2" in reason and "held" in reason for p in problems for reason in p["reasons"]), problems)

    def test_cgroup_counters_missing_from_the_samples_fail_instead_of_passing_vacuously(self):
        samples = load_samples(3, detail=False)
        self.assertIn("backends_used_their_cpu_quota_in_every_window", self.failing(self.verdict(samples=samples)))
        self.assertIn("backends_held_their_pid_limit_in_every_window", self.failing(self.verdict(samples=samples)))

    def test_pids_must_stay_at_the_limit_minus_a_small_margin_in_every_window_and_the_fractions_are_recorded(self):
        self.assertEqual(soak.PIDS_REACHED_FRACTION, 0.85)
        low = load_samples(3, pids=40)  # 40 of 64: the first smoke
        verdict = self.verdict(samples=low)
        self.assertEqual(self.failing(verdict), {"backends_held_their_pid_limit_in_every_window"})
        achieved = self.verdict()["achieved_fractions"]
        self.assertAlmostEqual(achieved["pids_peak"]["min"], 58 / 64, places=3)
        self.assertAlmostEqual(achieved["pids_window_min"]["min"], 58 / 64, places=3)
        self.assertAlmostEqual(achieved["memory_current_min"]["min"], 0.9, places=3)
        self.assertGreaterEqual(achieved["cpu_window_min"]["min"], 0.94)
        self.assertEqual(len(achieved["pids_peak"]["per_backend"]), 8)

    def test_a_pid_count_that_dropped_in_one_window_fails_although_the_run_peak_reached_the_limit(self):
        samples = load_samples(3)
        for sample in samples:  # window 2 only: backend 6 held 40 of 64 PIDs, having peaked at 60 before
            if LOAD_START_MS + PERIOD_MS < sample["at_ms"] <= LOAD_START_MS + 2 * PERIOD_MS:
                row = sample["backends"]["per_backend"][6]
                row["pids_current"], row["pids_peak"] = 40, 60
        verdict = self.verdict(samples=samples)
        self.assertEqual(self.failing(verdict), {"backends_held_their_pid_limit_in_every_window"})
        detail = self.check(verdict, "backends_held_their_pid_limit_in_every_window")["detail"]
        self.assertTrue(any(p["window"] == 2 and p["backend"] == "container-6" for p in detail["problems"]), detail)

    def test_the_runner_journal_may_grow_by_at_most_the_bound_over_the_load(self):
        self.assertEqual(soak.JOURNAL_GROWTH_LIMIT_BYTES, 256 * 1024 ** 2)
        ok = self.verdict()
        self.assertTrue(ok["passed"], self.failing(ok))
        detail = self.check(ok, "runner_journal_growth_within_bound")["detail"]
        self.assertEqual(detail["limit_bytes"], 256 * 1024 ** 2)
        self.assertGreater(detail["growth_bytes"], 0)
        # 3 minutes at 2 MiB a second is 360 MiB: over the bound.
        fast = self.verdict(samples=load_samples(3, journal_rate=2 * 1024 ** 2))
        self.assertEqual(self.failing(fast), {"runner_journal_growth_within_bound"})
        # No reading at all is a failed check, not a pass.
        blind = self.verdict(samples=load_samples(3, journal_rate=None))
        self.assertEqual(self.failing(blind), {"runner_journal_growth_within_bound"})

    def test_ops08_evidence_requires_the_full_duration_and_a_pass(self):
        smoke = self.verdict(minutes=5)
        self.assertTrue(smoke["passed"])
        self.assertFalse(smoke["counts_as_ops08_evidence"])
        evidence = child_evidence(windows=120)
        full = soak.evaluate(evidence, load_samples(120), parent_passed=True, unit_tests={"TestTaskQuotasRefuseAtTheirOwnBoundary": True},
                             event="workflow_dispatch")
        self.assertTrue(full["passed"], self.failing(full))
        self.assertTrue(full["counts_as_ops08_evidence"])

    # ---- SEC-09: the task worker, the event delivery coordinator and a backend-originated flood, judged beside OPS-08 ----

    def test_the_step_summary_names_every_platform_work_check(self):
        evidence = child_evidence()
        text = soak.render_summary(evidence, self.verdict(evidence))
        for name in soak.PLATFORM_WORK_CHECKS:
            self.assertIn(name + ": pass", text)

    def test_the_platform_work_checks_are_in_the_verdict_and_so_in_the_evidence_decision(self):
        verdict = self.verdict(minutes=120, samples=load_samples(120), evidence=child_evidence(120))
        names = {c["name"] for c in verdict["checks"]}
        self.assertTrue(set(soak.PLATFORM_WORK_CHECKS) <= names, set(soak.PLATFORM_WORK_CHECKS) - names)
        self.assertTrue(verdict["counts_as_ops08_evidence"], self.failing(verdict))
        for name in soak.PLATFORM_WORK_CHECKS:
            evidence = child_evidence(120)
            del evidence["platform_work"]
            for window in evidence["windows"]:
                for key in ("tasks", "events", "quota", "backend_flood", "workload_submit"):
                    window.pop(key)
            broken = self.verdict(minutes=120, samples=load_samples(120), evidence=evidence)
            self.assertFalse(broken["counts_as_ops08_evidence"])
            self.assertIn(name, self.failing(broken))

    def test_evidence_without_the_platform_work_block_fails_every_platform_work_check(self):
        evidence = child_evidence()
        del evidence["platform_work"]
        for window in evidence["windows"]:
            for key in ("tasks", "events", "quota", "backend_flood", "workload_submit"):
                window.pop(key)
        self.assertEqual(self.failing(self.verdict(evidence)), set(soak.PLATFORM_WORK_CHECKS))

    # ---- SEC-09 (review): cross-tenant starvation, and evidence that is missing must fail ----

    def test_the_workload_tenants_must_submit_through_the_route_within_bound_in_every_window(self):
        name = "workload_tenants_submit_through_the_route_in_every_window"
        self.assertIn(name, soak.PLATFORM_WORK_CHECKS)
        self.assertTrue(self.verdict(child_evidence())["passed"])
        failures = {
            "too few": window_workload_submit(requests=soak.WORKLOAD_SUBMIT_MIN_PER_WINDOW - 1),
            "busy": window_workload_submit(classes={"200": 30, "429:app_platform_busy": 6}),
            "quota refusal": window_workload_submit(classes={"200": 30, "429:task_capacity": 6}),
            "transport error": window_workload_submit(classes={"200": 30, "transport": 6}),
            "p95 over": window_workload_submit(latency_ms={"count": 36, "p50_ms": 25, "p95_ms": soak.WORKLOAD_SUBMIT_P95_BOUND_MS + 1, "max_ms": 900}),
            "max over": window_workload_submit(latency_ms={"count": 36, "p50_ms": 25, "p95_ms": 90, "max_ms": soak.WORKLOAD_SUBMIT_MAX_BOUND_MS + 1}),
            "no p95": window_workload_submit(latency_ms={"count": 36, "p50_ms": 25, "max_ms": 240}),
            "no latency": {"requests": 36, "answered": 36, "classes": {"200": 36}},
            "no classes": {"requests": 36, "answered": 36, "latency_ms": {"count": 36, "p50_ms": 25, "p95_ms": 90, "max_ms": 240}},
        }
        for label, value in failures.items():
            evidence = child_evidence()
            evidence["windows"][1]["workload_submit"] = value
            self.assertEqual(self.failing(self.verdict(evidence)), {name}, label)
        evidence = child_evidence()
        del evidence["windows"][1]["workload_submit"]
        self.assertIn(name, self.failing(self.verdict(evidence)))
        evidence = child_evidence()
        evidence["windows"][1]["workload_submit"] = window_workload_submit(
            requests=soak.WORKLOAD_SUBMIT_MIN_PER_WINDOW, latency_ms={"count": 24, "p50_ms": 25, "p95_ms": soak.WORKLOAD_SUBMIT_P95_BOUND_MS, "max_ms": soak.WORKLOAD_SUBMIT_MAX_BOUND_MS},
            classes={"200": soak.WORKLOAD_SUBMIT_MIN_PER_WINDOW})
        self.assertTrue(self.verdict(evidence)["passed"], self.failing(self.verdict(evidence)))

    def test_the_forged_credential_flood_must_run_and_every_answer_must_be_a_refusal(self):
        name = "forged_identity_flood_refused_in_every_window"
        self.assertIn(name, soak.PLATFORM_WORK_CHECKS)
        self.assertTrue(self.verdict(child_evidence())["passed"])
        failures = {
            "too few": {"forged_requests": soak.FORGED_FLOOD_MIN_PER_WINDOW - 1, "forged_classes": {"404:app_platform_task_unavailable": soak.FORGED_FLOOD_MIN_PER_WINDOW - 1}},
            "accepted": {"forged_requests": 640, "forged_classes": {"404:app_platform_task_unavailable": 600, "200": 40}},
            "quota answer": {"forged_requests": 640, "forged_classes": {"404:app_platform_task_unavailable": 600, "429:task_capacity": 40}},
            "server error": {"forged_requests": 640, "forged_classes": {"404:app_platform_task_unavailable": 600, "500:db_error": 40}},
            "transport": {"forged_requests": 640, "forged_classes": {"404:app_platform_task_unavailable": 600, "transport": 40}},
            "no classes": {"forged_requests": 640},
        }
        for label, changes in failures.items():
            evidence = child_evidence()
            flood = evidence["windows"][1]["backend_flood"]
            flood.pop("forged_classes", None)
            flood.update(changes)
            self.assertEqual(self.failing(self.verdict(evidence)), {name}, label)
        evidence = child_evidence()
        evidence["windows"][1]["backend_flood"].pop("forged_requests")
        self.assertEqual(self.failing(self.verdict(evidence)), {name})
        evidence = child_evidence()
        evidence["windows"][1]["backend_flood"].update(forged_requests=700, forged_classes={"404:app_platform_task_unavailable": 600, "429:app_platform_busy": 100})
        self.assertTrue(self.verdict(evidence)["passed"], self.failing(self.verdict(evidence)))

    def test_a_missing_measurement_fails_instead_of_defaulting_to_zero(self):
        cases = (
            ("no_task_lost_or_stuck", lambda e: e["windows"][0]["tasks"].pop("oldest_open_age_ms")),
            ("task_latency_within_bound", lambda e: e["platform_work"]["loaded_latency_ms"].pop("p95_ms")),
            ("task_latency_within_bound", lambda e: e["platform_work"]["loaded_latency_ms"].pop("max_ms")),
            ("event_queue_bounded", lambda e: e["windows"][1]["events"].pop("max_inflight_per_subscription")),
        )
        for name, damage in cases:
            evidence = child_evidence()
            damage(evidence)
            self.assertIn(name, self.failing(self.verdict(evidence)), name)

    def test_the_saturated_tenants_must_hold_their_quota_and_none_of_their_tasks_may_run(self):
        name = "task_quota_enforced_in_every_window"
        evidence = child_evidence()
        evidence["platform_work"]["saturated_tasks_run"] = 1
        self.assertEqual(self.failing(self.verdict(evidence)), {name})
        evidence = child_evidence()
        del evidence["platform_work"]["saturated_tasks_run"]
        self.assertEqual(self.failing(self.verdict(evidence)), {name})
        evidence = child_evidence()
        evidence["windows"][1]["quota"] = window_quota(saturated_active={"sat-0": soak.TASK_INSTALLATION_QUOTA, "sat-1": soak.TASK_INSTALLATION_QUOTA - 1})
        self.assertEqual(self.failing(self.verdict(evidence)), {name})
        evidence = child_evidence()
        evidence["windows"][1]["quota"] = window_quota(saturated_active={"sat-0": soak.TASK_INSTALLATION_QUOTA})
        self.assertEqual(self.failing(self.verdict(evidence)), {name})
        evidence = child_evidence()
        evidence["windows"][1]["quota"] = {"max_installation_active": soak.TASK_INSTALLATION_QUOTA}
        self.assertEqual(self.failing(self.verdict(evidence)), {name})

    def test_the_task_worker_must_complete_tasks_in_every_window(self):
        evidence = child_evidence()
        evidence["windows"][1]["tasks"] = window_tasks(succeeded=soak.TASK_WORKER_MIN_COMPLETED_PER_WINDOW - 1)
        verdict = self.verdict(evidence)
        self.assertEqual(self.failing(verdict), {"task_worker_ran_in_every_window"})
        self.assertEqual(self.check(verdict, "task_worker_ran_in_every_window")["detail"]["windows_below_the_minimum"], [2])
        evidence["windows"][1]["tasks"] = window_tasks(succeeded=soak.TASK_WORKER_MIN_COMPLETED_PER_WINDOW)
        self.assertTrue(self.verdict(evidence)["passed"])
        evidence["windows"][2]["tasks"] = window_tasks(worker_alive=False)
        self.assertEqual(self.failing(self.verdict(evidence)), {"task_worker_ran_in_every_window"})

    def test_a_task_that_failed_was_lost_or_never_finished_fails(self):
        for state in ("failed", "uncertain", "review_required", "cancelled"):
            evidence = child_evidence()
            evidence["windows"][0]["tasks"] = window_tasks(**{state: 1})
            self.assertEqual(self.failing(self.verdict(evidence)), {"no_task_lost_or_stuck"}, state)
            evidence = child_evidence()
            evidence["platform_work"]["tasks_total"]["by_state"][state] = 1
            self.assertEqual(self.failing(self.verdict(evidence)), {"no_task_lost_or_stuck"}, state)
        evidence = child_evidence()
        evidence["platform_work"]["tasks_total"]["created"] += 1
        self.assertEqual(self.failing(self.verdict(evidence)), {"no_task_lost_or_stuck"})
        evidence = child_evidence()
        evidence["platform_work"]["tasks_total"]["admit_errors"] = 1
        self.assertEqual(self.failing(self.verdict(evidence)), {"no_task_lost_or_stuck"})
        evidence = child_evidence()
        evidence["platform_work"]["drain"] = {"seconds": 180, "open_after": 2, "timed_out": True}
        self.assertEqual(self.failing(self.verdict(evidence)), {"no_task_lost_or_stuck", "event_queue_bounded"})

    def test_a_due_task_waiting_longer_than_the_stuck_age_fails_in_the_window_it_was_seen(self):
        evidence = child_evidence()
        evidence["windows"][1]["tasks"] = window_tasks(oldest_open_age_ms=soak.TASK_STUCK_MAX_AGE_MS)
        self.assertTrue(self.verdict(evidence)["passed"])
        evidence["windows"][1]["tasks"] = window_tasks(oldest_open_age_ms=soak.TASK_STUCK_MAX_AGE_MS + 1)
        verdict = self.verdict(evidence)
        self.assertEqual(self.failing(verdict), {"no_task_lost_or_stuck"})
        self.assertEqual(self.check(verdict, "no_task_lost_or_stuck")["detail"]["windows_with_a_stuck_task"], [2])

    def test_task_latency_is_judged_on_the_pooled_p95_and_the_slowest_task(self):
        evidence = child_evidence()
        evidence["platform_work"]["loaded_latency_ms"] = task_stats(count=200, p95=soak.TASK_LATENCY_P95_BOUND_MS)
        self.assertTrue(self.verdict(evidence)["passed"])
        evidence["platform_work"]["loaded_latency_ms"] = task_stats(count=200, p95=soak.TASK_LATENCY_P95_BOUND_MS + 1)
        self.assertEqual(self.failing(self.verdict(evidence)), {"task_latency_within_bound"})
        evidence["platform_work"]["loaded_latency_ms"] = task_stats(count=200, maximum=soak.TASK_LATENCY_MAX_BOUND_MS + 1)
        self.assertEqual(self.failing(self.verdict(evidence)), {"task_latency_within_bound"})
        evidence["platform_work"]["loaded_latency_ms"] = task_stats(count=0)
        self.assertEqual(self.failing(self.verdict(evidence)), {"task_latency_within_bound"})

    def test_a_slow_window_is_a_diagnostic_and_does_not_decide_the_pooled_latency(self):
        evidence = child_evidence()
        evidence["windows"][1]["tasks"] = window_tasks(latency_ms=task_stats(p95=soak.TASK_LATENCY_P95_BOUND_MS * 3))
        verdict = self.verdict(evidence)
        self.assertTrue(verdict["passed"])
        self.assertEqual(self.check(verdict, "task_latency_within_bound")["detail"]["worst_window"]["minute"], 2)

    def test_the_installation_task_quota_is_enforced_in_every_window_with_a_typed_refusal(self):
        evidence = child_evidence()
        evidence["windows"][1]["quota"] = window_quota(max_installation_active=soak.TASK_INSTALLATION_QUOTA + 1)
        verdict = self.verdict(evidence)
        self.assertEqual(self.failing(verdict), {"task_quota_enforced_in_every_window"})
        self.assertEqual(self.check(verdict, "task_quota_enforced_in_every_window")["detail"]["windows_over_the_quota"], [2])
        evidence = child_evidence()
        evidence["windows"][2]["backend_flood"] = window_backend_flood(classes={"200": 120, "429:rate_limited": 2280})
        verdict = self.verdict(evidence)
        self.assertEqual(self.failing(verdict), {"task_quota_enforced_in_every_window"})
        self.assertEqual(self.check(verdict, "task_quota_enforced_in_every_window")["detail"]["windows_without_a_typed_refusal"], [3])

    def test_every_event_must_reach_every_subscribed_app_at_least_once(self):
        evidence = child_evidence()
        evidence["platform_work"]["events_total"]["delivered"] -= 1
        self.assertEqual(self.failing(self.verdict(evidence)), {"event_delivered_at_least_once"})
        evidence = child_evidence()
        evidence["platform_work"]["events_total"].update(captured=0, expected_deliveries=0, delivered=0)
        self.assertEqual(self.failing(self.verdict(evidence)), {"event_delivered_at_least_once"})
        evidence = child_evidence()
        evidence["windows"][1]["events"] = window_events(delivered=0)
        verdict = self.verdict(evidence)
        self.assertEqual(self.failing(verdict), {"event_delivered_at_least_once"})
        self.assertEqual(self.check(verdict, "event_delivered_at_least_once")["detail"]["windows_without_a_delivery"], [2])

    def test_the_event_queue_is_bounded_by_the_subscription_inflight_cap_and_drains(self):
        evidence = child_evidence()
        evidence["windows"][1]["events"] = window_events(max_inflight_per_subscription=soak.EVENT_MAX_INFLIGHT)
        self.assertTrue(self.verdict(evidence)["passed"])
        evidence["windows"][1]["events"] = window_events(max_inflight_per_subscription=soak.EVENT_MAX_INFLIGHT + 1)
        verdict = self.verdict(evidence)
        self.assertEqual(self.failing(verdict), {"event_queue_bounded"})
        self.assertEqual(self.check(verdict, "event_queue_bounded")["detail"]["windows_over_the_bound"], [2])
        evidence = child_evidence()
        evidence["platform_work"]["drain"] = {"seconds": 180, "open_after": 4, "timed_out": True}
        self.assertIn("event_queue_bounded", self.failing(self.verdict(evidence)))

    def test_an_event_queue_that_grows_every_window_fails_even_inside_the_cap(self):
        evidence = child_evidence(windows=9)
        for index, window in enumerate(evidence["windows"]):
            window["events"] = window_events(max_inflight_per_subscription=1 + index)
        verdict = self.verdict(evidence, minutes=9)
        self.assertEqual(self.failing(verdict), {"event_queue_bounded"})
        self.assertTrue(self.check(verdict, "event_queue_bounded")["detail"]["growing"])

    def test_event_refusals_are_typed_workload_subscriptions_stay_active_and_the_saturated_one_is_fenced_for_its_quota(self):
        for change in ({"capture_errors": 1},):
            evidence = child_evidence()
            evidence["platform_work"]["events_total"].update(change)
            self.assertEqual(self.failing(self.verdict(evidence)), {"event_refusals_are_typed"}, change)
        evidence = child_evidence()
        evidence["windows"][0]["events"] = window_events(capture_errors=1)
        self.assertEqual(self.failing(self.verdict(evidence)), {"event_refusals_are_typed"})
        evidence = child_evidence()
        evidence["platform_work"]["subscriptions"][0].update(state="review_required", reason="inflight_limit")
        self.assertEqual(self.failing(self.verdict(evidence)), {"event_refusals_are_typed"})
        evidence = child_evidence()
        evidence["platform_work"]["subscriptions"][6].update(reason="something_else")
        self.assertEqual(self.failing(self.verdict(evidence)), {"event_refusals_are_typed"})
        evidence = child_evidence()
        evidence["platform_work"]["subscriptions"][6].update(state="active", reason="")
        self.assertEqual(self.failing(self.verdict(evidence)), {"event_refusals_are_typed"})

    def test_the_backend_task_flood_must_be_answered_or_refused_with_a_typed_error_in_every_window(self):
        evidence = child_evidence()
        evidence["windows"][1]["backend_flood"] = window_backend_flood(requests=soak.BACKEND_FLOOD_MIN_PER_WINDOW - 1, answered=1)
        verdict = self.verdict(evidence)
        self.assertEqual(self.failing(verdict), {"backend_task_flood_answered_or_typed_in_every_window"})
        self.assertEqual(self.check(verdict, "backend_task_flood_answered_or_typed_in_every_window")["detail"]["windows_below_the_minimum"], [2])
        for classes in ({"200": 100, "429:task_capacity": 2000, "500:": 2}, {"200": 100, "429:task_capacity": 2000, "transport": 1},
                        {"200": 100, "429:task_capacity": 2000, "404:app_platform_task_unavailable": 5}, {"200": 100, "429:task_capacity": 2000, "429:": 3}):
            evidence = child_evidence()
            evidence["windows"][2]["backend_flood"] = window_backend_flood(classes=classes)
            verdict = self.verdict(evidence)
            self.assertEqual(self.failing(verdict), {"backend_task_flood_answered_or_typed_in_every_window"}, classes)
        evidence = child_evidence()
        evidence["windows"][2]["backend_flood"] = window_backend_flood(classes={"200": 100, "429:task_capacity": 2000, "429:rate_limited": 50})
        self.assertTrue(self.verdict(evidence)["passed"])
        evidence = child_evidence()
        evidence["windows"][2]["backend_flood"] = window_backend_flood(
            classes={"200": 100, "429:task_capacity": 40, "429:app_platform_busy": 9000})
        self.assertTrue(self.verdict(evidence)["passed"])
        evidence = child_evidence()
        evidence["windows"][2]["backend_flood"] = window_backend_flood(
            classes={"200": 100, "429:task_capacity": 40, "429:app_platform_busy": 9000, "503:app_platform_busy": 1})
        self.assertEqual(self.failing(self.verdict(evidence)), {"backend_task_flood_answered_or_typed_in_every_window"})

    def test_the_backend_task_flood_must_stay_alive(self):
        for change in ({"alive": False}, {"heartbeat_age_ms": soak.FLOOD_HEARTBEAT_MAX_AGE_MS + 1}):
            evidence = child_evidence()
            evidence["windows"][2]["backend_flood"] = window_backend_flood(**change)
            verdict = self.verdict(evidence)
            self.assertEqual(self.failing(verdict), {"backend_task_flood_alive_throughout"}, change)

    def test_the_backend_flood_runs_under_the_unchanged_ops08_bounds_which_still_decide_the_other_tenants_latency(self):
        self.assertEqual(soak.HOME_P95_BOUND_US, 50_000)
        self.assertEqual(soak.REPORT_P95_BOUND_US, 50_000)
        self.assertEqual(soak.DISPATCH_P95_BOUND_US, 25_000)
        self.assertEqual(soak.BATCH_START_P95_BOUND_US, 100_000)
        evidence = child_evidence(home=soak.HOME_P95_BOUND_US)
        self.assertEqual(self.failing(self.verdict(evidence)), {"home_p95_within_absolute_bound"})
        verdict = self.verdict()
        self.assertEqual(self.check(verdict, "backend_task_flood_alive_throughout")["detail"]["windows_judged_under_the_flood"], 3)
        self.assertIn("no_runner_crash", self.failing(self.verdict(exit_ok=False)))


class VariantTests(unittest.TestCase):
    def test_a_diagnostic_variant_never_counts_as_ops08_evidence_even_when_it_passes_for_two_hours(self):
        evidence = child_evidence(windows=120)
        samples = load_samples(120)
        kwargs = {"parent_passed": True, "unit_tests": {"TestTaskQuotasRefuseAtTheirOwnBoundary": True}, "event": "workflow_dispatch"}
        self.assertTrue(soak.evaluate(evidence, samples, **kwargs)["counts_as_ops08_evidence"])
        verdict = soak.evaluate(evidence, samples, variant="memory-db", **kwargs)
        self.assertTrue(verdict["passed"])
        self.assertFalse(verdict["counts_as_ops08_evidence"])
        self.assertEqual(verdict["variant"], "memory-db")

    def test_the_variant_is_a_closed_list(self):
        self.assertEqual(soak.soak_variant(""), "")
        self.assertEqual(soak.soak_variant("memory-db,no-batch-probes"), "memory-db,no-batch-probes")
        self.assertEqual(soak.soak_variant("skip-image-store"), "skip-image-store")
        self.assertEqual(soak.soak_variant("no-backend-flood,no-task-worker"), "no-backend-flood,no-task-worker")
        for bad in ("memory", "no-batch-probes;x", "a b"):
            with self.assertRaises(RuntimeError):
                soak.soak_variant(bad)


class DatabaseTests(unittest.TestCase):
    def kwargs(self):
        return {"parent_passed": True, "unit_tests": {"TestTaskQuotasRefuseAtTheirOwnBoundary": True}, "event": "workflow_dispatch"}

    def test_only_a_postgres_run_counts_as_ops08_evidence(self):
        # Owner decision 2026-11-14: the evidence is a run on PostgreSQL; a SQLite run is a known-limit measurement.
        samples = load_samples(120)
        self.assertTrue(soak.evaluate(child_evidence(windows=120), samples, **self.kwargs())["counts_as_ops08_evidence"])
        for driver in ("sqlite", "", None):
            evidence = child_evidence(windows=120)
            evidence["db_driver"] = driver
            verdict = soak.evaluate(evidence, samples, **self.kwargs())
            self.assertTrue(verdict["passed"], driver)
            self.assertFalse(verdict["counts_as_ops08_evidence"], driver)
            self.assertEqual(verdict["db_driver"], driver or "")

    def test_the_database_choice_is_a_closed_list(self):
        self.assertEqual(soak.soak_database(""), "postgres")
        self.assertEqual(soak.soak_database("postgres"), "postgres")
        self.assertEqual(soak.soak_database("sqlite-file"), "sqlite-file")
        for bad in ("sqlite", "mysql", "postgres;x"):
            with self.assertRaises(RuntimeError):
                soak.soak_database(bad)


def write_process(proc, pid, comm, args, ticks, cgroup="0::/system.slice/x.service"):
    folder = Path(proc) / str(pid)
    folder.mkdir(parents=True, exist_ok=True)
    (folder / "stat").write_text(f"{pid} ({comm}) S 1 1 1 0 -1 0 0 0 0 0 {ticks[0]} {ticks[1]} 0 0 20 0 1 0 1 1 1\n")
    (folder / "cmdline").write_bytes("\0".join(args).encode() + b"\0")
    (folder / "cgroup").write_text(cgroup + "\n")


class CPUBreakdownTests(unittest.TestCase):
    def test_processes_are_grouped_by_what_they_are(self):
        c = soak.classify_process
        self.assertEqual(c("portal-soak-nati", ["/tmp/x/portal.test", "-test.run=^TestPlatformCapacitySoakChild$"]), "portal")
        self.assertEqual(c("portal.test", ["/tmp/x/portal.test", "-test.run=^TestPlatformCapacitySoakFlood$", "-test.v"]), "flood")
        self.assertEqual(c("portal.test", ["/tmp/x/portal.test", "-test.run=^TestPlatformRunnerServiceChild$"]), "runner")
        self.assertEqual(c("portal.test", ["/tmp/x/portal.test", "-test.run=^TestPlatformCapacitySoak$"]), "other")
        for comm in ("postgres", "postmaster"):
            self.assertEqual(c(comm, ["postgres: 16/main: portal reports_soak 127.0.0.1(5) idle"]), "postgres")
        self.assertEqual(c("dockerd", ["/usr/bin/dockerd"]), "docker")
        self.assertEqual(c("containerd", ["/usr/bin/containerd"]), "docker")
        self.assertEqual(c("containerd-shim", ["/usr/bin/containerd-shim-runc-v2"]), "docker")
        self.assertEqual(c("bash", ["bash"]), "other")

    def test_a_sample_reports_each_groups_share_of_the_host_and_the_rest_as_other(self):
        with tempfile.TemporaryDirectory() as root:
            proc = Path(root) / "proc"
            proc.mkdir()
            (proc / "meminfo").write_text(MEMINFO)
            (proc / "loadavg").write_text("0.5 0.4 0.3 4/600 99\n")
            (Path(root) / "mount").mkdir()
            child = ["/t/portal.test", "-test.run=^TestPlatformCapacitySoakChild$"]
            flood = ["/t/portal.test", "-test.run=^TestPlatformCapacitySoakFlood$"]
            ticks = {"n": 0}

            def lay(n):
                # four CPUs at 100 ticks a second: a host second is 400 ticks. Between samples (10 s) the host spends 2000 busy of 4000.
                (proc / "stat").write_text(f"cpu  {1000 + n * 2000} 0 0 {9000 + n * 2000} 0 0 0 0 0 0\n")
                write_process(proc, 100, "portal.test", child, (100 + n * 400, 0))      # 400 ticks = 10% of 4000
                write_process(proc, 101, "portal.test", flood, (50 + n * 200, 0))       # 5%
                write_process(proc, 102, "postgres", ["postgres: x"], (0 + n * 100, 100 * n))  # 5%
                write_process(proc, 103, "portal.test", ["/t/portal.test", "-test.run=^TestPlatformRunnerServiceChild$"], (n * 40, 0))  # 1%
                write_process(proc, 104, "dockerd", ["dockerd"], (n * 60, 0))
                write_process(proc, 105, "containerd-shim", ["containerd-shim"], (n * 20, 0))
                write_process(proc, 106, "other-thing", ["x"], (n * 80, 0))              # 2%
                write_process(proc, 107, "soak-app", ["/soak-app"], (n * 700, 0), cgroup="0::/system.slice/docker-" + "f" * 64 + ".scope")  # inside a backend

            def run(*args):
                return "" if args[:2] == ("docker", "ps") else (_ for _ in ()).throw(AssertionError(args))

            moments = iter([1000.0, 1010.0])
            sampler = soak.Sampler(run=run, proc=proc, mountpoint=Path(root) / "mount", image_id="sha256:" + "b" * 64, clock=lambda: next(moments),
                                   cpus=4, ticks_per_second=100)
            lay(0)
            first = sampler.sample()
            self.assertNotIn("cpu_groups", first)
            lay(1)
            sampler._backend_full_ids = {"f" * 64}
            # The backends' summed cgroup CPU: 400 millis of the host's 4000 is 10%.
            sampler._backends = lambda now: ({"total": 8, "running": 8, "restarting": 0, "exited": 0, "oom_killed": 0, "restart_count": 0,
                                              "memory_bytes": 0, "cpu_millis": 400.0, "pids": 0, "cpu_source": "cgroup"}, 0)
            second = sampler.sample()
        shares = second["cpu_groups"]
        self.assertEqual(round(shares["host_busy"], 1), 50.0)
        self.assertEqual({k: round(v, 1) for k, v in shares.items() if k not in ("host_busy", "other")},
                         {"portal": 10.0, "flood": 5.0, "postgres": 5.0, "runner": 1.0, "docker": 2.0, "backends": 10.0})
        self.assertEqual(round(shares["other"], 1), 50.0 - 33.0 + 0.0)


def self_kwargs():
    return {"parent_passed": True, "unit_tests": {"TestTaskQuotasRefuseAtTheirOwnBoundary": True}, "event": "workflow_dispatch"}


class StallTests(unittest.TestCase):
    PRESSURE = ("some avg10=1.50 avg60=0.80 avg300=0.20 total=1000\nfull avg10=0.25 avg60=0.10 avg300=0.05 total=400\n")

    def test_pressure_files_give_some_and_full_with_avg10_and_the_running_total(self):
        self.assertEqual(soak.parse_pressure(self.PRESSURE), {"some": {"avg10": 1.5, "total_us": 1000}, "full": {"avg10": 0.25, "total_us": 400}})
        # The host's cpu file has no full line on older kernels.
        self.assertEqual(soak.parse_pressure("some avg10=0.00 avg60=0.00 avg300=0.00 total=7\n"), {"some": {"avg10": 0.0, "total_us": 7}})
        with self.assertRaises(ValueError):
            soak.parse_pressure("garbage")

    def test_diskstats_are_read_by_device_number_and_turned_into_utilisation_and_await(self):
        # major minor name reads rd_merged rd_sectors rd_ms writes wr_merged wr_sectors wr_ms in_flight io_ticks weighted
        text = " 8  1 sda1 100 0 800 50 200 0 1600 400 0 700 450\n 7 0 loop0 1 0 8 1 2 0 16 2 0 3 3\n"
        stats = soak.parse_diskstats(text)
        self.assertEqual(stats["8:1"]["name"], "sda1")
        self.assertEqual((stats["8:1"]["reads"], stats["8:1"]["writes"], stats["8:1"]["io_ticks_ms"]), (100, 200, 700))
        later = soak.parse_diskstats(" 8  1 sda1 150 0 1200 90 300 0 2400 900 1 1700 1100\n")
        delta = soak.disk_delta(stats["8:1"], later["8:1"], seconds=10.0)
        self.assertEqual(delta["utilisation_percent"], 10.0)                 # 1000 ms of I/O in 10 s
        self.assertAlmostEqual(delta["await_ms"], (40 + 500) / 150, places=3)  # (read ms + write ms) over I/Os
        self.assertEqual(delta["iops"], 15.0)
        self.assertEqual(delta["write_kib_per_second"], 40.0)                 # 800 sectors of 512 bytes in 10 s
        self.assertEqual(soak.disk_delta(stats["8:1"], stats["8:1"], seconds=10.0)["await_ms"], 0.0)

    def test_the_sample_carries_pressure_dirty_pages_and_the_cpu_wait_counters(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            proc = root / "proc"
            (proc / "pressure").mkdir(parents=True)
            (proc / "meminfo").write_text(MEMINFO + "Dirty: 2048 kB\nWriteback: 512 kB\n")
            (proc / "stat").write_text("cpu  100 0 100 700 100 0 0 0 0 0\nprocs_running 5\nprocs_blocked 2\n")
            (proc / "loadavg").write_text("0.5 0.4 0.3 4/600 99\n")
            for name in ("cpu", "io", "memory"):
                (proc / "pressure" / name).write_text(self.PRESSURE)
            (root / "mount").mkdir()
            sampler = soak.Sampler(run=lambda *a: "", proc=proc, mountpoint=root / "mount", image_id="sha256:" + "b" * 64, clock=lambda: 1.0, cpus=4, ticks_per_second=100)
            sample = sampler.sample()
        self.assertEqual(sample["pressure"]["host"]["io"]["some"]["avg10"], 1.5)
        self.assertEqual(sample["pressure"]["host"]["memory"]["full"]["total_us"], 400)
        self.assertEqual(sample["vm"], {"procs_running": 5, "procs_blocked": 2, "dirty_kib": 2048, "writeback_kib": 512})

    def test_a_group_cgroups_pressure_is_read_where_the_layout_says_it_lives(self):
        with tempfile.TemporaryDirectory() as root:
            fs = Path(root)
            scope = fs / "system.slice" / "soak-portal.scope"
            scope.mkdir(parents=True)
            for name in ("cpu", "io", "memory"):
                (scope / f"{name}.pressure").write_text(self.PRESSURE)
            got = soak.read_group_pressure(fs, "/system.slice/soak-portal.scope")
            self.assertEqual(got["cpu"]["some"]["avg10"], 1.5)
            self.assertEqual(soak.read_group_pressure(fs, "/system.slice/missing.scope"), {})

    def test_the_storage_layout_says_which_devices_the_volumes_live_on_and_whether_they_share_a_disk(self):
        devices = {"8:1": {"name": "sda1"}, "7:0": {"name": "loop0"}}
        layout = soak.storage_layout({"postgres_data": "8:1", "xfs_mount": "7:0", "xfs_backing_file": "8:1", "docker_root": "8:1"}, devices,
                                     {"postgres_data_dir": "/var/lib/postgresql/16/main"})
        self.assertEqual(layout["postgres_data"]["device"], "sda1")
        self.assertEqual(layout["xfs_mount"]["device"], "loop0")
        self.assertTrue(layout["postgres_and_xfs_backing_share_a_disk"])
        other = soak.storage_layout({"postgres_data": "8:1", "xfs_mount": "7:0", "xfs_backing_file": "8:16", "docker_root": "8:1"}, devices, {})
        self.assertFalse(other["postgres_and_xfs_backing_share_a_disk"])

    def test_postgres_wal_sync_counters_become_commit_rate_and_the_mean_fsync_time(self):
        before = soak.parse_pg_wal("1000|200|40.0\n")
        after = soak.parse_pg_wal("1600|500|190.0\n")
        self.assertEqual(soak.pg_delta(before, after, seconds=30.0), {"commits_per_second": 20.0, "wal_syncs_per_second": 10.0, "mean_wal_sync_ms": 0.5})
        self.assertEqual(soak.pg_delta(before, before, seconds=30.0)["mean_wal_sync_ms"], 0.0)

    def test_the_verdict_summarises_each_stall_signal_for_the_idle_period_and_the_load(self):
        evidence = child_evidence(windows=3)
        evidence["idle_windows"] = [{"minute": 0, "started_ms": LOAD_START_MS - 90_000, "ended_ms": LOAD_START_MS - 30_000}]
        samples = load_samples(3)
        for sample in samples:
            idle = sample["at_ms"] <= LOAD_START_MS
            some = lambda v: {"some": {"avg10": v, "total_us": int(v * 1000)}, "full": {"avg10": v / 2, "total_us": int(v * 500)}}
            sample["pressure"] = {"host": {"cpu": some(1.0 if idle else 30.0), "io": some(0.5 if idle else 20.0), "memory": some(0.0)},
                                  "portal": {"cpu": some(0.1 if idle else 40.0), "io": some(0.0 if idle else 2.0), "memory": some(0.0)}}
            sample["vm"] = {"procs_running": 1 if idle else 9, "procs_blocked": 0, "dirty_kib": 100 if idle else 90_000, "writeback_kib": 0}
            sample["disk"] = {"postgres_data": {"utilisation_percent": 1.0 if idle else 95.0, "await_ms": 0.5 if idle else 12.0, "iops": 3.0, "write_kib_per_second": 10.0}}
            sample["pg"] = {"commits_per_second": 2.0 if idle else 400.0, "wal_syncs_per_second": 1.0, "mean_wal_sync_ms": 0.4 if idle else 6.0}
        summary = soak.evaluate(evidence, samples, **self_kwargs())["stall_summary"]
        self.assertEqual(summary["idle"]["pressure.portal.cpu.some.avg10"], {"mean": 0.1, "max": 0.1})
        self.assertEqual(summary["loaded"]["pressure.portal.cpu.some.avg10"], {"mean": 40.0, "max": 40.0})
        self.assertEqual(summary["loaded"]["disk.postgres_data.await_ms"]["max"], 12.0)
        self.assertEqual(summary["loaded"]["pg.mean_wal_sync_ms"]["mean"], 6.0)
        self.assertEqual(summary["loaded"]["vm.dirty_kib"]["max"], 90_000)


class TopologyTests(unittest.TestCase):
    def test_eevdf_is_the_scheduler_from_kernel_6_6(self):
        for kernel, want in (("7.0.0-1017-azure", True), ("6.8.0-1030-azure", True), ("6.6.12", True), ("6.5.0", False), ("5.15.0-1", False)):
            self.assertEqual(soak.uses_eevdf(kernel), want, kernel)
        self.assertFalse(soak.uses_eevdf("garbage"))

    def test_the_topology_says_whether_cpu_threads_share_a_core_and_who_shares_with_cpu_0(self):
        shared = soak.smt_layout({"cpu0": "0-1", "cpu1": "0-1", "cpu2": "2-3", "cpu3": "2-3"})
        self.assertTrue(shared["smt"])
        self.assertEqual(shared["cpu0_siblings"], "0-1")
        alone = soak.smt_layout({"cpu0": "0", "cpu1": "1", "cpu2": "2", "cpu3": "3"})
        self.assertFalse(alone["smt"])
        self.assertEqual(alone["cpu0_siblings"], "0")

    def test_the_topology_reads_the_sibling_lists_from_sysfs(self):
        with tempfile.TemporaryDirectory() as root:
            for cpu, siblings in (("cpu0", "0,2"), ("cpu1", "1,3"), ("cpu2", "0,2"), ("cpu3", "1,3")):
                folder = Path(root) / cpu / "topology"
                folder.mkdir(parents=True)
                (folder / "thread_siblings_list").write_text(siblings + "\n")
            (Path(root) / "cpufreq").mkdir()
            self.assertEqual(soak.read_thread_siblings(root), {"cpu0": "0,2", "cpu1": "1,3", "cpu2": "0,2", "cpu3": "1,3"})


class CPUModeTests(unittest.TestCase):
    def test_the_modes_of_the_host_cpu_time_split_user_system_interrupts_and_steal(self):
        before = "cpu  100 0 100 700 0 0 0 0 0 0\n"
        after = "cpu  300 0 200 1100 50 20 60 70 0 0\n"
        modes = soak.cpu_modes(soak.parse_cpu_modes(before), soak.parse_cpu_modes(after))
        # 900 ticks passed: 200 user, 100 system, 400 idle, 50 iowait, 20 irq, 60 softirq, 70 steal
        self.assertEqual(modes, {"user": 22.22, "nice": 0.0, "system": 11.11, "idle": 44.44, "iowait": 5.56, "irq": 2.22, "softirq": 6.67, "steal": 7.78})

    def test_a_sample_carries_the_modes(self):
        with tempfile.TemporaryDirectory() as root:
            proc = Path(root) / "proc"
            proc.mkdir()
            (proc / "meminfo").write_text(MEMINFO)
            (proc / "loadavg").write_text("0.5 0.4 0.3 4/600 99\n")
            (Path(root) / "mount").mkdir()
            moments = iter([1.0, 11.0])
            sampler = soak.Sampler(run=lambda *a: "", proc=proc, mountpoint=Path(root) / "mount", image_id="x", clock=lambda: next(moments), cpus=4, ticks_per_second=100)
            (proc / "stat").write_text("cpu  100 0 100 700 0 0 0 0 0 0\n")
            sampler.sample()
            (proc / "stat").write_text("cpu  300 0 200 1100 50 20 60 70 0 0\n")
            self.assertEqual(sampler.sample()["cpu_modes"]["steal"], 7.78)


class DetailTests(unittest.TestCase):
    def test_comm_names_lose_their_numbers_so_kernel_threads_group(self):
        self.assertEqual(soak.normalise_comm("kworker/u8:3"), "kworker/uN:N")
        self.assertEqual(soak.normalise_comm("ksoftirqd/2"), "ksoftirqd/N")
        self.assertEqual(soak.normalise_comm("loop0"), "loopN")
        self.assertEqual(soak.normalise_comm("jbd2/sda1-8"), "jbdN/sdaN-N")
        self.assertEqual(soak.normalise_comm("dockerd"), "dockerd")

    def test_a_sample_splits_docker_and_the_rest_by_command_name(self):
        with tempfile.TemporaryDirectory() as root:
            proc = Path(root) / "proc"
            proc.mkdir()
            (proc / "meminfo").write_text(MEMINFO)
            (proc / "loadavg").write_text("0.5 0.4 0.3 4/600 99\n")
            (Path(root) / "mount").mkdir()
            moments = iter([1.0, 11.0])
            sampler = soak.Sampler(run=lambda *a: "", proc=proc, mountpoint=Path(root) / "mount", image_id="sha256:" + "b" * 64, clock=lambda: next(moments), cpus=4, ticks_per_second=100)

            def lay(n):
                (proc / "stat").write_text(f"cpu  {2000 * n} 0 0 {8000 + 2000 * n} 0 0 0 0 0 0\ncpu0 0 0 0 0 0 0 0 0\n")
                write_process(proc, 20, "dockerd", ["dockerd"], (n * 100, 0))              # 100 ticks of 4000 (10 s, 4 cpus): 2.5%
                write_process(proc, 21, "containerd-shim", ["containerd-shim"], (n * 40, 0))  # 1%
                write_process(proc, 22, "kworker/u8:3", [], (n * 80, 0))                     # 2%
                write_process(proc, 23, "kworker/u8:5", [], (n * 40, 0))                     # 1%, the same group
                write_process(proc, 24, "bash", ["bash"], (n * 20, 0))

            lay(0)
            sampler.sample()
            lay(1)
            detail = sampler.sample()["cpu_detail"]
        self.assertEqual({k: round(v, 2) for k, v in detail.items()},
                         {"docker.dockerd": 2.5, "docker.containerd-shim": 1.0, "other.kworker/uN:N": 3.0, "other.bash": 0.5})

    def test_the_verdict_lists_the_top_consumers_inside_docker_and_other(self):
        evidence = child_evidence(windows=3)
        samples = load_samples(3)
        for sample in samples:
            sample["cpu_detail"] = {"docker.dockerd": 2.0, "docker.containerd": 1.0, "other.kworker/uN:N": 5.0, "other.loopN": 0.5}
        for sample in samples:
            sample["cpu_modes"] = {"user": 40.0, "system": 20.0, "softirq": 5.0, "steal": 6.0, "idle": 29.0}
        verdict = soak.evaluate(evidence, samples, **self_kwargs())
        self.assertEqual(verdict["cpu_modes_loaded"]["steal"], 6.0)
        top = verdict["cpu_detail_top"]
        self.assertEqual(top["other"][0], {"name": "kworker/uN:N", "percent_of_host": 5.0})
        self.assertEqual([x["name"] for x in top["docker"]], ["dockerd", "containerd"])


class PlatformWeightTests(unittest.TestCase):
    def test_the_weight_is_empty_or_one_to_ten_thousand(self):
        self.assertEqual(soak.soak_platform_cpu_weight(""), "")
        self.assertEqual(soak.soak_platform_cpu_weight("1000"), "1000")
        for bad in ("0", "10001", "x", "-1", " 1000", "1e3"):
            with self.assertRaises(RuntimeError):
                soak.soak_platform_cpu_weight(bad)

    def test_a_weighted_run_is_not_evidence(self):
        kwargs = {"parent_passed": True, "unit_tests": {"TestTaskQuotasRefuseAtTheirOwnBoundary": True}, "event": "workflow_dispatch"}
        verdict = soak.evaluate(child_evidence(windows=120), load_samples(120), platform_weight="1000", **kwargs)
        self.assertTrue(verdict["passed"])
        self.assertFalse(verdict["counts_as_ops08_evidence"])
        self.assertEqual(verdict["platform_cpu_weight"], "1000")


class PinPlatformTests(unittest.TestCase):
    def test_the_pin_set_is_the_reserved_cpus_the_runner_keeps_free_of_apps(self):
        self.assertEqual(soak.pin_cpu_set(""), "0")        # the runner default reserves one CPU
        self.assertEqual(soak.pin_cpu_set("1"), "0")
        self.assertEqual(soak.pin_cpu_set("2"), "0-1")
        self.assertEqual(soak.pin_cpu_set("3"), "0-2")
        with self.assertRaises(RuntimeError):
            soak.pin_cpu_set("0")                          # nothing reserved, nothing to pin to

    def test_the_pin_flag_is_a_closed_boolean(self):
        self.assertFalse(soak.soak_pin_platform(""))
        self.assertTrue(soak.soak_pin_platform("true"))
        self.assertFalse(soak.soak_pin_platform("false"))
        with self.assertRaises(RuntimeError):
            soak.soak_pin_platform("yes")

    def test_a_pinned_run_is_not_evidence(self):
        kwargs = {"parent_passed": True, "unit_tests": {"TestTaskQuotasRefuseAtTheirOwnBoundary": True}, "event": "workflow_dispatch"}
        evidence, samples = child_evidence(windows=120), load_samples(120)
        verdict = soak.evaluate(evidence, samples, pin_platform=True, **kwargs)
        self.assertTrue(verdict["passed"])
        self.assertFalse(verdict["counts_as_ops08_evidence"])
        self.assertTrue(verdict["pin_platform"])

    def test_the_postgres_unit_is_found_by_its_name(self):
        out = "postgresql@16-main.service loaded active running PostgreSQL Cluster 16-main\n"
        self.assertEqual(soak.postgres_unit(lambda *a: out), "postgresql@16-main.service")
        with self.assertRaises(RuntimeError):
            soak.postgres_unit(lambda *a: "")

    def test_the_layout_records_each_processs_actual_affinity(self):
        with tempfile.TemporaryDirectory() as root:
            proc, fs = Path(root) / "proc", Path(root) / "cgroup"
            proc.mkdir()
            write_process(proc, 10, "portal.test", ["/t/portal.test", "-test.run=^TestPlatformCapacitySoakChild$"], (1, 1), cgroup="0::/system.slice/soak-portal.scope")
            (proc / "10" / "status").write_text("Name:\tportal.test\nCpus_allowed:\t3\nCpus_allowed_list:\t0-1\n")
            (fs / "system.slice/soak-portal.scope").mkdir(parents=True)
            layout = soak.cgroup_layout(proc, fs, set(), "/system.slice")
        self.assertEqual(layout["portal"]["cpus_allowed"], "0-1")


class ReservedCPUTests(unittest.TestCase):
    def test_the_reserved_cpus_override_is_empty_or_a_small_integer(self):
        self.assertEqual(soak.soak_reserved_cpus(""), "")
        for good in ("0", "1", "2", "3"):
            self.assertEqual(soak.soak_reserved_cpus(good), good)
        for bad in ("-1", "x", "1.5", "01", "65", " 2"):
            with self.assertRaises(RuntimeError):
                soak.soak_reserved_cpus(bad)

    def test_an_override_of_the_reserved_cpus_is_evidence_only_when_it_equals_the_default(self):
        self.assertEqual(soak.RUNNER_DEFAULT_RESERVED_CPUS, 1)
        kwargs = {"parent_passed": True, "unit_tests": {"TestTaskQuotasRefuseAtTheirOwnBoundary": True}, "event": "workflow_dispatch"}
        evidence, samples = child_evidence(windows=120), load_samples(120)
        self.assertTrue(soak.evaluate(evidence, samples, reserved_cpus="1", **kwargs)["counts_as_ops08_evidence"])
        verdict = soak.evaluate(evidence, samples, reserved_cpus="2", **kwargs)
        self.assertTrue(verdict["passed"])
        self.assertFalse(verdict["counts_as_ops08_evidence"])
        self.assertEqual(verdict["reserved_cpus_override"], "2")


class CpusetTests(unittest.TestCase):
    def test_per_cpu_busy_comes_from_the_per_cpu_lines_of_proc_stat(self):
        before = "cpu  0 0 0 0 0 0 0 0\ncpu0 100 0 0 900 0 0 0 0\ncpu1 0 0 0 1000 0 0 0 0\ncpu2 500 0 100 400 0 0 0 0\n"
        after = "cpu  0 0 0 0 0 0 0 0\ncpu0 150 0 0 1850 0 0 0 0\ncpu1 900 0 0 1100 0 0 0 0\ncpu2 1000 0 100 900 0 0 0 0\n"
        self.assertEqual(soak.per_cpu_busy(soak.parse_per_cpu(before), soak.parse_per_cpu(after)), {"cpu0": 5.0, "cpu1": 90.0, "cpu2": 50.0})
        self.assertEqual(soak.per_cpu_busy({}, soak.parse_per_cpu(after)), {})

    def test_the_ready_line_names_the_reserved_and_the_app_cpus(self):
        line = "2026/11/14 12:30:17 app-runner ready profile=x platform=linux/amd64 capacity=memory_mib=3995 cpu_millis=2000 reserved_cpus=0 app_cpus=1-3 refined_rows=0"
        self.assertEqual(soak.parse_ready_cpus(line), {"reserved_cpus": "0", "app_cpus": "1-3"})
        self.assertEqual(soak.parse_ready_cpus("no ready line"), {})

    def test_the_sample_carries_per_cpu_busy_and_the_backends_effective_cpuset(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            proc = root / "proc"
            proc.mkdir()
            (proc / "meminfo").write_text(MEMINFO)
            (proc / "loadavg").write_text("0.5 0.4 0.3 4/600 99\n")
            (root / "mount").mkdir()
            moments = iter([1.0, 11.0])
            sampler = soak.Sampler(run=lambda *a: "", proc=proc, mountpoint=root / "mount", image_id="sha256:" + "b" * 64, clock=lambda: next(moments), cpus=2, ticks_per_second=100)
            (proc / "stat").write_text("cpu  0 0 0 0 0 0 0 0\ncpu0 0 0 0 100 0 0 0 0\ncpu1 0 0 0 100 0 0 0 0\n")
            sampler.sample()
            (proc / "stat").write_text("cpu  0 0 0 0 0 0 0 0\ncpu0 0 0 0 200 0 0 0 0\ncpu1 100 0 0 100 0 0 0 0\n")
            second = sampler.sample()
        self.assertEqual(second["cpu_per_core"], {"cpu0": 0.0, "cpu1": 100.0})

    def test_the_cgroup_reader_records_the_cpuset_a_container_really_runs_on(self):
        with tempfile.TemporaryDirectory() as root:
            base = Path(root) / ("docker-" + "a" * 64 + ".scope")
            base.mkdir()
            for name, text in {"cpu.stat": "usage_usec 5\n", "memory.current": "1\n", "memory.max": "2\n", "cpu.max": "24000 100000\n", "pids.current": "1\n",
                               "pids.max": "64\n", "cpuset.cpus.effective": "1-3\n"}.items():
                (base / name).write_text(text)
            sampler = soak.Sampler(run=lambda *a: "", proc=Path(root), mountpoint=Path(root), image_id="x", clock=lambda: 1.0, cgroup_root=root, cpus=4, ticks_per_second=100)
            row = sampler._read_cgroup("a" * 64)
        self.assertEqual(row["cpuset_effective"], "1-3")

    def test_the_verdict_summarises_each_cpu_for_idle_and_the_load(self):
        evidence = child_evidence(windows=3)
        evidence["idle_windows"] = [{"minute": 0, "started_ms": LOAD_START_MS - 90_000, "ended_ms": LOAD_START_MS - 30_000}]
        samples = load_samples(3)
        for sample in samples:
            idle = sample["at_ms"] <= LOAD_START_MS
            sample["cpu_per_core"] = {"cpu0": 1.0 if idle else 40.0, "cpu1": 1.0 if idle else 97.0}
        summary = soak.evaluate(evidence, samples, **self_kwargs())["per_cpu_busy"]
        self.assertEqual(summary["idle"]["cpu0"], {"mean": 1.0, "max": 1.0})
        self.assertEqual(summary["loaded"]["cpu1"], {"mean": 97.0, "max": 97.0})


class CgroupLayoutTests(unittest.TestCase):
    def test_the_layout_names_each_process_group_its_cgroup_and_the_cpu_weight_along_its_path(self):
        with tempfile.TemporaryDirectory() as root:
            proc, fs = Path(root) / "proc", Path(root) / "cgroup"
            proc.mkdir()
            child = ["/t/portal.test", "-test.run=^TestPlatformCapacitySoakChild$"]
            flood = ["/t/portal.test", "-test.run=^TestPlatformCapacitySoakFlood$"]
            runner = ["/t/portal.test", "-test.run=^TestPlatformRunnerServiceChild$"]
            write_process(proc, 10, "portal.test", child, (1, 1), cgroup="0::/system.slice/soak-portal.scope")
            write_process(proc, 11, "portal.test", flood, (1, 1), cgroup="0::/system.slice/soak-portal.scope")
            write_process(proc, 12, "portal.test", runner, (1, 1), cgroup="0::/system.slice/soak-runner.scope")
            write_process(proc, 13, "postgres", ["postgres: x"], (1, 1), cgroup="0::/system.slice/system-postgresql.slice/postgresql@16-main.service")
            for path, weight in (("system.slice", "100"), ("system.slice/soak-portal.scope", "100"), ("system.slice/soak-runner.scope", "100"),
                                 ("system.slice/system-postgresql.slice", "100"), ("system.slice/system-postgresql.slice/postgresql@16-main.service", "100"),
                                 ("system.slice/docker-" + "f" * 64 + ".scope", "100")):
                (fs / path).mkdir(parents=True)
                (fs / path / "cpu.weight").write_text(weight + "\n")
            (fs / "system.slice/soak-portal.scope/cpu.weight").write_text("50\n")
            layout = soak.cgroup_layout(proc, fs, {"f" * 64}, "/system.slice")
        self.assertEqual(layout["portal"]["cgroup"], "/system.slice/soak-portal.scope")
        self.assertEqual(layout["portal"]["cpu_weights"], {"/system.slice": "100", "/system.slice/soak-portal.scope": "50"})
        self.assertEqual(layout["flood"]["cgroup"], "/system.slice/soak-portal.scope")
        self.assertEqual(layout["runner"]["cgroup"], "/system.slice/soak-runner.scope")
        self.assertEqual(layout["postgres"]["cpu_weights"]["/system.slice/system-postgresql.slice/postgresql@16-main.service"], "100")
        self.assertEqual(layout["backend"]["cgroup"], "/system.slice/docker-" + "f" * 64 + ".scope")
        self.assertEqual(layout["backend"]["cpu_weights"]["/system.slice/docker-" + "f" * 64 + ".scope"], "100")
        self.assertEqual(layout["portal"]["pid"], 10)

    def test_a_missing_group_is_reported_as_absent_not_invented(self):
        with tempfile.TemporaryDirectory() as root:
            proc = Path(root) / "proc"
            proc.mkdir()
            layout = soak.cgroup_layout(proc, Path(root) / "cgroup", set(), "/system.slice")
        self.assertEqual(layout, {})

    def test_the_scope_probe_names_what_failed_instead_of_faking_a_layout(self):
        calls = []

        def failing(*args):
            calls.append(args)
            raise RuntimeError("Failed to connect to bus")

        with self.assertRaises(RuntimeError) as raised:
            soak.require_systemd_scopes(failing)
        self.assertIn("systemd-run", str(raised.exception))
        self.assertIn("--scope", " ".join(calls[0]))
        self.assertIn("CPUWeight=100", " ".join(calls[0]))
        soak.require_systemd_scopes(lambda *a: "")


class CPUBudgetTests(unittest.TestCase):
    def kwargs(self):
        return {"parent_passed": True, "unit_tests": {"TestTaskQuotasRefuseAtTheirOwnBoundary": True}, "event": "workflow_dispatch"}

    def test_the_cpu_percent_is_empty_or_one_to_one_hundred(self):
        self.assertEqual(soak.soak_cpu_percent(""), "")
        self.assertEqual(soak.soak_cpu_percent("37"), "37")
        for bad in ("0", "101", "x", "-5", "37.5", "50 "):
            with self.assertRaises(RuntimeError):
                soak.soak_cpu_percent(bad)

    def test_an_override_of_the_runner_cpu_budget_is_evidence_only_when_it_equals_the_default(self):
        self.assertEqual(soak.RUNNER_DEFAULT_CPU_PERCENT, 50)
        samples = load_samples(120)
        evidence = child_evidence(windows=120)
        self.assertTrue(soak.evaluate(evidence, samples, **self.kwargs())["counts_as_ops08_evidence"])
        self.assertTrue(soak.evaluate(evidence, samples, cpu_percent="50", **self.kwargs())["counts_as_ops08_evidence"])
        for override in ("37", "25"):
            verdict = soak.evaluate(evidence, samples, cpu_percent=override, **self.kwargs())
            self.assertTrue(verdict["passed"])
            self.assertFalse(verdict["counts_as_ops08_evidence"], override)
            self.assertEqual(verdict["cpu_percent_override"], override)

    def test_the_verdict_breaks_the_cpu_down_by_group_for_the_idle_period_and_every_window(self):
        evidence = child_evidence(windows=3)
        evidence["idle_windows"] = [{"minute": 0, "started_ms": LOAD_START_MS - 90_000, "ended_ms": LOAD_START_MS - 30_000}]
        samples = load_samples(3)
        for sample in samples:
            at = sample["at_ms"]
            idle = at <= LOAD_START_MS
            sample["cpu_groups"] = {"host_busy": 5.0 if idle else 80.0, "portal": 1.0 if idle else 8.0, "flood": 0.0 if idle else 10.0,
                                    "postgres": 0.5 if idle else 12.0, "runner": 0.1, "docker": 0.2, "backends": 0.0 if idle else 48.0, "other": 3.2 if idle else 1.7}
        flood = evidence["windows"][0]["flood"]
        flood.update(backend_requests_ok=600, backend_requests_busy=7200)
        breakdown = soak.evaluate(evidence, samples, **self_kwargs())["cpu_breakdown"]
        self.assertEqual(breakdown["idle"]["portal"], 1.0)
        self.assertEqual(len(breakdown["windows"]), 3)
        self.assertEqual(breakdown["windows"][0]["flood"], 10.0)
        self.assertEqual(breakdown["windows"][0]["host_busy"], 80.0)
        self.assertEqual(breakdown["flood_rates"][0], {"minute": 1, "admitted_per_second": 10.0, "refused_per_second": 120.0})
        text = soak.render_summary(evidence, soak.evaluate(evidence, samples, **self_kwargs()))
        self.assertIn("CPU by group", text)

    def test_host_busy_and_the_backends_cpu_are_recorded_over_the_load(self):
        samples = load_samples(3)
        for i, sample in enumerate(samples):
            sample["host_cpu_percent"] = 80.0 if i % 2 else 60.0
        verdict = soak.evaluate(child_evidence(windows=3), samples, **self.kwargs())
        busy = verdict["host_busy_percent"]
        self.assertEqual(busy["max"], 80.0)
        self.assertTrue(60.0 < busy["mean"] < 80.0)
        self.assertEqual(verdict["backend_cpu_millis_mean"], 1900.0)


class UnitTestResultTests(unittest.TestCase):
    def test_go_test_json_results_are_read_by_test_name(self):
        lines = "\n".join(json.dumps(e) for e in [
            {"Action": "run", "Package": "p", "Test": "TestA"}, {"Action": "pass", "Package": "p", "Test": "TestA"},
            {"Action": "fail", "Package": "p", "Test": "TestB"}, {"Action": "pass", "Package": "p"},
            {"Action": "pass", "Package": "q", "Test": "TestC/sub"}, {"Action": "skip", "Package": "q", "Test": "TestD"}])
        self.assertEqual(soak.parse_go_test_json(lines + "\nnot json\n"), {"TestA": True, "TestB": False, "TestC/sub": True, "TestD": False})

    def test_a_missing_file_is_no_results(self):
        self.assertEqual(soak.read_go_test_json("/nonexistent/unit-tests.json"), {})
        self.assertEqual(soak.read_go_test_json(""), {})


class ConfigurationTests(unittest.TestCase):
    def test_image_must_be_an_owned_loopback_digest(self):
        self.assertEqual(soak.soak_image(IMAGE), IMAGE)
        for bad in ["", "registry.example/app@sha256:" + "a" * 64, "localhost:0/portal-soak-backend@sha256:" + "a" * 64,
                    "localhost:5000/portal-ready-fixture@sha256:" + "a" * 64, "localhost:5000/portal-soak-backend:latest"]:
            with self.assertRaises(RuntimeError):
                soak.soak_image(bad)

    def test_duration_is_bounded_to_what_the_step_timeout_allows(self):
        self.assertEqual(soak.MAX_DURATION_MINUTES, 140)
        self.assertEqual(soak.duration_minutes("120"), 120)
        self.assertEqual(soak.duration_minutes("140"), 140)
        for bad in ["0", "-1", "x", "", "141", "180", "181", "3.5"]:
            with self.assertRaises(RuntimeError):
                soak.duration_minutes(bad)

    def test_the_image_store_probe_runs_natively_only_when_the_daemon_disk_can_hold_two_images_and_keeps_headroom(self):
        need = soak.IMAGE_STORE_NATIVE_BYTES
        self.assertEqual(need, 8 * 1024 ** 3 + soak.IMAGE_STORE_HEADROOM_BYTES)
        self.assertEqual(soak.image_store_mode(need), ("native", ""))
        mode, reason = soak.image_store_mode(need - 1)
        self.assertEqual(mode, "unit")
        self.assertIn(str(need - 1), reason)
        self.assertIn("free", reason)
        self.assertEqual(soak.image_store_mode(None)[0], "unit")

    def test_the_baseline_is_the_same_for_the_smoke_and_the_acceptance_run(self):
        # The pull request smoke runs the dispatch configuration apart from its duration (and the diagnosis it adds).
        self.assertEqual(soak.baseline_seconds(5), 180)
        self.assertEqual(soak.baseline_seconds(120), 180)
        self.assertEqual(soak.baseline_seconds(140), 180)

    def test_disposable_host_is_required(self):
        with self.assertRaises(RuntimeError):
            soak.require_disposable_environment({"GITHUB_ACTIONS": "true"}, euid=0, platform="linux", which=lambda tool, path=None: "/bin/" + tool)
        with self.assertRaises(RuntimeError):
            soak.require_disposable_environment({"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted"}, euid=1000, platform="linux", which=lambda tool, path=None: "/bin/" + tool)
        with self.assertRaises(RuntimeError):
            soak.require_disposable_environment({"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted"}, euid=0, platform="linux", which=lambda tool, path=None: None)
        soak.require_disposable_environment({"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted"}, euid=0, platform="linux", which=lambda tool, path=None: "/bin/" + tool)


class ReferenceHostTests(unittest.TestCase):
    """The owner's reference VM (a self-hosted runner) may run the soak only from a manual dispatch that asked for it (doc 171)."""

    def which(self, tool, path=None):
        return "/bin/" + tool

    def check_env(self, **env):
        return soak.require_disposable_environment(env, euid=0, platform="linux", which=self.which)

    def reference(self, **changes):
        env = {"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "self-hosted", "APP_CAPACITY_SOAK_HOST": "reference-vm", "GITHUB_EVENT_NAME": "workflow_dispatch"}
        env.update(changes)
        return env

    def test_a_self_hosted_runner_is_accepted_only_for_the_reference_vm_from_a_dispatch(self):
        self.check_env(**self.reference())
        self.check_env(GITHUB_ACTIONS="true", RUNNER_ENVIRONMENT="github-hosted")

    def test_every_other_self_hosted_combination_is_refused(self):
        refused = {
            "no host flag": self.reference(APP_CAPACITY_SOAK_HOST=""),
            "github flag": self.reference(APP_CAPACITY_SOAK_HOST="github"),
            "other flag": self.reference(APP_CAPACITY_SOAK_HOST="my-laptop"),
            "pull request": self.reference(GITHUB_EVENT_NAME="pull_request"),
            "push": self.reference(GITHUB_EVENT_NAME="push"),
            "schedule": self.reference(GITHUB_EVENT_NAME="schedule"),
            "no event": self.reference(GITHUB_EVENT_NAME=""),
            "not actions": self.reference(GITHUB_ACTIONS=""),
            "github-hosted with the reference flag": self.reference(RUNNER_ENVIRONMENT="github-hosted"),
            "unknown runner environment": self.reference(RUNNER_ENVIRONMENT="something-else"),
            "no runner environment": {"GITHUB_ACTIONS": "true", "APP_CAPACITY_SOAK_HOST": "reference-vm", "GITHUB_EVENT_NAME": "workflow_dispatch"},
        }
        for label, env in refused.items():
            with self.assertRaises(RuntimeError, msg=label):
                self.check_env(**env)

    def test_the_reference_vm_still_needs_root_linux_and_the_tools(self):
        with self.assertRaises(RuntimeError):
            soak.require_disposable_environment(self.reference(), euid=1000, platform="linux", which=self.which)
        with self.assertRaises(RuntimeError):
            soak.require_disposable_environment(self.reference(), euid=0, platform="darwin", which=self.which)
        with self.assertRaises(RuntimeError):
            soak.require_disposable_environment(self.reference(), euid=0, platform="linux", which=lambda tool, path=None: None)

    def test_the_host_is_recorded_from_the_environment_and_proc(self):
        cpuinfo = "processor\t: 0\nmodel name\t: Intel(R) Xeon(R) E-2236 CPU @ 3.40GHz\nprocessor\t: 1\nmodel name\t: Intel(R) Xeon(R) E-2236 CPU @ 3.40GHz\n"
        meminfo = "MemTotal:       16384000 kB\nMemFree:         1000 kB\n"
        host = soak.host_identity({"RUNNER_ENVIRONMENT": "self-hosted", "APP_CAPACITY_SOAK_HOST": "reference-vm", "RUNNER_NAME": "sec09-ref-1"}, cpuinfo, meminfo, 4)
        self.assertEqual(host, {"kind": "reference-vm", "runner_name": "sec09-ref-1", "cpu_model": "Intel(R) Xeon(R) E-2236 CPU @ 3.40GHz", "nproc": 4, "mem_total_kib": 16384000})
        hosted = soak.host_identity({"RUNNER_ENVIRONMENT": "github-hosted", "RUNNER_NAME": "GitHub Actions 7"}, "", "", 4)
        self.assertEqual((hosted["kind"], hosted["cpu_model"], hosted["mem_total_kib"]), ("github-hosted", "unknown", 0))
        self.assertEqual(soak.host_identity({"RUNNER_ENVIRONMENT": "self-hosted"}, cpuinfo, meminfo, 4)["kind"], "unknown")

    def test_a_passing_reference_vm_dispatch_counts_exactly_like_a_github_hosted_one(self):
        base = dict(GITHUB_EVENT_NAME="workflow_dispatch", GITHUB_SHA="a" * 40)
        hosted = evidence_verdict(evidence_environment(RUNNER_ENVIRONMENT="github-hosted", **base))
        reference = evidence_verdict(evidence_environment(RUNNER_ENVIRONMENT="self-hosted", APP_CAPACITY_SOAK_HOST="reference-vm", RUNNER_NAME="sec09-ref-1", **base))
        self.assertTrue(hosted["counts_as_ops08_evidence"], hosted["evidence_blockers"])
        self.assertTrue(reference["counts_as_ops08_evidence"], reference["evidence_blockers"])
        # The same checks with the same outcome; only the latency bound itself and the host kind it was judged for differ (owner decision
        # 2026-11-16: the reference VM is judged at 1.5 times the absolute bounds).
        def without_bounds(checks):
            out = []
            for check in checks:
                detail = {k: v for k, v in check["detail"].items()
                          if k not in ("bound_us", "threshold_us", "bound_host_kind", "window_thresholds")} if isinstance(check.get("detail"), dict) else check.get("detail")
                out.append({**check, "detail": detail})
            return out
        self.assertEqual(without_bounds(reference["checks"]), without_bounds(hosted["checks"]))
        self.assertEqual([c["passed"] for c in reference["checks"]], [c["passed"] for c in hosted["checks"]])
        self.assertEqual(reference["host_identity"]["kind"], "reference-vm")
        self.assertEqual(reference["host_identity"]["runner_name"], "sec09-ref-1")
        self.assertEqual(hosted["host_identity"]["kind"], "github-hosted")

    def test_a_reference_vm_run_that_is_not_a_dispatch_is_never_evidence(self):
        verdict = evidence_verdict(evidence_environment(RUNNER_ENVIRONMENT="self-hosted", APP_CAPACITY_SOAK_HOST="reference-vm", GITHUB_EVENT_NAME="pull_request"))
        self.assertFalse(verdict["counts_as_ops08_evidence"])
        self.assertTrue(any("workflow_dispatch" in b for b in verdict["evidence_blockers"]))


_FAST = {"writes": 200, "bytes_per_write": 4096, "p50_ms": 0.1, "p95_ms": 0.3, "max_ms": 0.9}


class ReferenceDiskPrecheckTests(unittest.TestCase):
    """The reference VM's disk is slower than a GitHub-hosted one by the virtio journal, inherent to its host (doc 171): probe median 1.05 to 1.25 ms.
    The precheck only decides whether the disk is slower than this host normally is; the latency bounds are not touched."""

    SLOW = dict(_FAST, p50_ms=1.2, p95_ms=1.6)

    def test_the_reference_vm_is_judged_at_1_2_ms_where_a_github_hosted_run_is_inconclusive(self):
        reference = soak.disk_precheck(1.5, self.SLOW, host_kind="reference-vm")
        self.assertFalse(reference["tripped"], reference)
        self.assertFalse(soak.probe_trips(self.SLOW, host_kind="reference-vm"))
        for kind in ("github-hosted", "unknown", "", None):
            hosted = soak.disk_precheck(0.06, self.SLOW, host_kind=kind)
            self.assertTrue(hosted["tripped"], kind)
            self.assertTrue(soak.probe_trips(self.SLOW, host_kind=kind), kind)
        self.assertTrue(soak.disk_precheck(0.06, self.SLOW)["tripped"], "the default is the strict threshold")
        self.assertTrue(soak.probe_trips(self.SLOW))

    def test_the_reference_vm_is_inconclusive_above_2_ms_probe_or_4_ms_wal_sync(self):
        self.assertFalse(soak.disk_precheck(4.0, dict(_FAST, p50_ms=2.0), host_kind="reference-vm")["tripped"])
        probe = soak.disk_precheck(0.06, dict(_FAST, p50_ms=2.01), host_kind="reference-vm")
        self.assertTrue(probe["tripped"])
        self.assertTrue(soak.probe_trips(dict(_FAST, p50_ms=2.01), host_kind="reference-vm"))
        wal = soak.disk_precheck(4.01, self.SLOW, host_kind="reference-vm")
        self.assertTrue(wal["tripped"])
        self.assertIn("4.0", wal["reasons"][0])

    def test_the_thresholds_the_run_used_and_the_host_kind_are_in_the_precheck(self):
        reference = soak.disk_precheck(0.06, _FAST, host_kind="reference-vm")
        self.assertEqual(reference["thresholds"], {"idle_wal_sync_ms": 4.0, "probe_p50_ms": 2.0})
        self.assertEqual(reference["host_kind"], "reference-vm")
        hosted = soak.disk_precheck(0.06, _FAST, host_kind="github-hosted")
        self.assertEqual(hosted["thresholds"], {"idle_wal_sync_ms": 2.0, "probe_p50_ms": 1.0})
        self.assertEqual(hosted["host_kind"], "github-hosted")

    def test_a_self_hosted_runner_that_is_not_the_reference_vm_never_gets_the_relaxed_thresholds(self):
        for env in ({"RUNNER_ENVIRONMENT": "self-hosted"}, {"RUNNER_ENVIRONMENT": "self-hosted", "APP_CAPACITY_SOAK_HOST": "github"},
                    {"RUNNER_ENVIRONMENT": "github-hosted", "APP_CAPACITY_SOAK_HOST": "reference-vm"}, {"APP_CAPACITY_SOAK_HOST": "reference-vm"}):
            kind = soak.host_identity(env, "", "", 4)["kind"]
            self.assertNotEqual(kind, "reference-vm", env)
            self.assertTrue(soak.disk_precheck(0.06, self.SLOW, host_kind=kind)["tripped"], env)

    def test_the_verdict_judges_a_reference_vm_run_at_1_2_ms_and_not_a_github_hosted_one(self):
        evidence = child_evidence(windows=120)
        probe = dict(self.SLOW, directory="/var/lib/postgresql/16", data_directory="/var/lib/postgresql/16/main")
        reference = soak.evaluate(evidence, load_samples(120), **GOOD, disk_probe=probe, event="workflow_dispatch", commit="a" * 40,
                                  host={"kind": "reference-vm", "runner_name": "r", "cpu_model": "x", "nproc": 4, "mem_total_kib": 1})
        self.assertEqual(reference["result"], soak.RESULT_PASSED, reference["precheck"])
        self.assertEqual(reference["precheck"]["host_kind"], "reference-vm")
        hosted = soak.evaluate(evidence, load_samples(120), **GOOD, disk_probe=probe, event="workflow_dispatch", commit="a" * 40,
                               host={"kind": "github-hosted"})
        self.assertEqual(hosted["result"], soak.RESULT_INCONCLUSIVE_SLOW_DISK)
        slower = soak.evaluate(evidence, load_samples(120), **GOOD, disk_probe=dict(probe, p50_ms=2.5), event="workflow_dispatch", commit="a" * 40,
                               host={"kind": "reference-vm"})
        self.assertEqual(slower["result"], soak.RESULT_INCONCLUSIVE_SLOW_DISK)


class ReferenceLatencyBoundTests(unittest.TestCase):
    """Owner decision 2026-11-16: the reference VM (4 vCPU KVM guest of an Intel Xeon E-2236, 8 GiB) is judged at 1.5 times the absolute latency
    bounds, because its idle latency alone is two to three times a GitHub-hosted AWS machine's; every other host keeps the bounds of 2026-11-15.
    The security and isolation checks, the load and the duration are the same on every host."""

    REFERENCE = {"home": 75_000, "report": 75_000, "dispatch": 37_500, "batch_start": 150_000}
    ORIGINAL = {"home": 50_000, "report": 50_000, "dispatch": 25_000, "batch_start": 100_000}

    def verdict(self, evidence, kind):
        return soak.evaluate(evidence, load_samples(3), parent_passed=True, unit_tests={"TestTaskQuotasRefuseAtTheirOwnBoundary": True},
                             event="workflow_dispatch", host={"kind": kind, "runner_name": "r", "cpu_model": "x", "nproc": 4, "mem_total_kib": 1})

    def failing(self, verdict):
        return {c["name"] for c in verdict["checks"] if not c["passed"]}

    def test_the_reference_vm_bounds_are_one_and_a_half_times_the_others(self):
        self.assertEqual(soak.absolute_bounds_us("reference-vm"), self.REFERENCE)
        for kind in ("github-hosted", "unknown", "", None):
            self.assertEqual(soak.absolute_bounds_us(kind), self.ORIGINAL, kind)
        self.assertEqual(soak.ABSOLUTE_P95_BOUND_US, self.ORIGINAL, "the bounds of 2026-11-15 are unchanged")

    def test_each_series_passes_at_the_reference_bound_and_fails_one_microsecond_beyond(self):
        for series, bound in self.REFERENCE.items():
            name = f"{series}_p95_within_absolute_bound"
            evidence = child_evidence()
            evidence["loaded"][series]["p95_us"] = bound
            self.assertTrue(self.verdict(evidence, "reference-vm")["passed"], series)
            evidence["loaded"][series]["p95_us"] = bound + 1
            self.assertEqual(self.failing(self.verdict(evidence, "reference-vm")), {name}, series)

    def test_a_github_hosted_run_is_still_judged_at_the_original_bounds(self):
        for series, bound in self.ORIGINAL.items():
            evidence = child_evidence()
            evidence["loaded"][series]["p95_us"] = bound + 1
            self.assertEqual(self.failing(self.verdict(evidence, "github-hosted")), {f"{series}_p95_within_absolute_bound"}, series)

    def test_the_verdict_records_the_bound_and_the_host_kind_it_was_judged_for(self):
        evidence = child_evidence()
        evidence["loaded"]["report"]["p95_us"] = 60_000
        verdict = self.verdict(evidence, "reference-vm")
        detail = next(c for c in verdict["checks"] if c["name"] == "report_p95_within_absolute_bound")["detail"]
        self.assertTrue(verdict["passed"], self.failing(verdict))
        self.assertEqual((detail["bound_us"], detail["threshold_us"], detail["bound_host_kind"]), (75_000, 75_000, "reference-vm"))
        hosted = next(c for c in self.verdict(evidence, "github-hosted")["checks"] if c["name"] == "report_p95_within_absolute_bound")["detail"]
        self.assertEqual((hosted["bound_us"], hosted["bound_host_kind"]), (50_000, "github-hosted"))

    def test_the_step_summary_states_the_bounds_the_run_was_judged_at(self):
        evidence = child_evidence()
        reference = soak.render_summary(evidence, self.verdict(evidence, "reference-vm"))
        self.assertIn("at most 75 ms for home and report, 37.5 ms for dispatch@200 and 150 ms for batch start", reference)
        self.assertIn("2026-11-16", reference)
        hosted = soak.render_summary(evidence, self.verdict(evidence, "github-hosted"))
        self.assertIn("at most 50 ms for home and report, 25 ms for dispatch@200 and 100 ms for batch start", hosted)

    def test_a_run_that_ends_before_the_load_states_the_bounds_of_its_own_host(self):
        slow = dict(_FAST, p50_ms=4.4)
        for kind, text in (("reference-vm", "at most 75 ms for home and report"), ("github-hosted", "at most 50 ms for home and report")):
            verdict = {"result": soak.RESULT_INCONCLUSIVE_SLOW_DISK, "passed": None, "checks": [],
                       "precheck": soak.disk_precheck(None, slow, host_kind=kind)}
            self.assertIn(text, soak.render_summary({"duration_minutes": 5}, verdict), kind)

    def test_a_self_hosted_runner_that_is_not_the_reference_vm_never_gets_the_reference_bounds(self):
        for env in ({"RUNNER_ENVIRONMENT": "self-hosted"}, {"RUNNER_ENVIRONMENT": "self-hosted", "APP_CAPACITY_SOAK_HOST": "github"},
                    {"RUNNER_ENVIRONMENT": "github-hosted", "APP_CAPACITY_SOAK_HOST": "reference-vm"}, {"APP_CAPACITY_SOAK_HOST": "reference-vm"}):
            kind = soak.host_identity(env, "", "", 4)["kind"]
            self.assertEqual(soak.absolute_bounds_us(kind), self.ORIGINAL, env)


class WorkflowHostGuardTests(unittest.TestCase):
    """No workflow may reach a self-hosted runner from anything but a manual dispatch that asked for the reference VM."""

    SANCTIONED = ("${{ (github.event_name == 'workflow_dispatch' && inputs.soak_host == 'reference-vm') && fromJSON('[\"self-hosted\",\"sec09-reference-vm\"]') || 'ubuntu-24.04' }}")

    def runs_on(self):
        found = []
        for path in sorted((WORKFLOW.parent).glob("*.yml")):
            for number, line in enumerate(path.read_text().splitlines(), 1):
                match = re.fullmatch(r"\s+runs-on:\s*(.*?)\s*", line)
                if match:
                    found.append((path.name, number, match.group(1)))
        return found

    def test_every_runner_is_a_github_hosted_label_or_the_one_sanctioned_expression(self):
        found = self.runs_on()
        self.assertGreater(len(found), 20)
        for name, number, value in found:
            if value == self.SANCTIONED:
                self.assertEqual(name, "app-project-quota-probe.yml", "the self-hosted expression belongs to the capacity soak only")
                continue
            self.assertRegex(value, r"ubuntu-(latest|24\.04|24\.04-arm)", f"{name}:{number} runs on {value!r}")
            self.assertNotIn("self-hosted", value)
            self.assertNotIn("${{", value, f"{name}:{number} resolves its runner at run time")

    def test_the_sanctioned_expression_is_on_the_capacity_soak_job_and_nowhere_else(self):
        text = WORKFLOW.read_text()
        job = text[text.index("\n  capacity-soak:"):]
        self.assertIn("runs-on: " + self.SANCTIONED, job.split("\n    steps:")[0])
        live = [line for line in text.splitlines() if "self-hosted" in line and not line.lstrip().startswith("#")]
        self.assertEqual(live, ["    runs-on: " + self.SANCTIONED], "self-hosted appears outside the capacity soak job's runs-on")

    def test_the_expression_only_selects_self_hosted_for_a_dispatch_that_asked_for_the_reference_vm(self):
        def resolve(event, soak_host):
            # the expression, evaluated: (event == dispatch && soak_host == reference-vm) ? self-hosted : ubuntu-24.04
            return "self-hosted" if (event == "workflow_dispatch" and soak_host == "reference-vm") else "ubuntu-24.04"
        for event in ("pull_request", "push", "schedule", "workflow_run", "release", ""):
            for host in ("", "github", "reference-vm"):
                self.assertEqual(resolve(event, host), "ubuntu-24.04")
        self.assertEqual(resolve("workflow_dispatch", "github"), "ubuntu-24.04")
        self.assertEqual(resolve("workflow_dispatch", "reference-vm"), "self-hosted")
        self.assertIn("github.event_name == 'workflow_dispatch' && inputs.soak_host == 'reference-vm'", self.SANCTIONED)

    def test_the_host_input_defaults_to_github_and_the_flag_follows_the_same_condition(self):
        text = WORKFLOW.read_text()
        self.assertIn("soak_host", workflow_dispatch_inputs())
        block = text[text.index("      soak_host:"):]
        block = block[:block.index("\n      ", 20) if False else 600]
        self.assertIn("options: [github, reference-vm]", block)
        self.assertIn("default: github", block)
        flag = job_env_line("APP_CAPACITY_SOAK_HOST")
        self.assertIn("github.event_name == 'workflow_dispatch' && inputs.soak_host == 'reference-vm' && 'reference-vm' || ''", flag)
        self.assertIn("APP_CAPACITY_SOAK_HOST", text[text.index("sudo --preserve-env="):text.index("python3 scripts/acceptance/capacity_soak.py")])


WORKFLOW = Path(__file__).resolve().parents[1] / ".github/workflows/app-project-quota-probe.yml"
GOOD = {"parent_passed": True, "unit_tests": {"TestTaskQuotasRefuseAtTheirOwnBoundary": True}}


def workflow_dispatch_inputs():
    """The input names of the workflow_dispatch trigger, read from the workflow file itself."""
    names, inside = [], False
    for line in WORKFLOW.read_text().splitlines():
        if line.startswith("  workflow_dispatch:"):
            inside = True
        elif inside and line.startswith("  ") and not line.startswith("   "):
            break
        elif inside and re.fullmatch(r"      [a-z_]+:", line):
            names.append(line.strip().rstrip(":"))
    return names


def job_env_line(name):
    job = WORKFLOW.read_text()
    job = job[job.index("\n  capacity-soak:"):]
    return next(line for line in job.splitlines() if line.strip().startswith(name + ":") and line.startswith("      APP_"))


# What the dispatcher sets for each diagnostic input, in the form the job passes it on: always a value that is not the default.
NON_DEFAULT = {"soak_db": "sqlite-file", "soak_cpu_percent": "37", "soak_reserved_cpus": "2", "soak_pin_platform": "true",
               "soak_platform_cpu_weight": "1000", "soak_runner_profile": "1", "soak_variant": "memory-db"}


def evidence_environment(**changes):
    env = {"GITHUB_EVENT_NAME": "workflow_dispatch", "GITHUB_SHA": "a" * 40, "GITHUB_REF_NAME": "feat/app-platform-capacity-soak"}
    env.update(changes)
    return env


def evidence_verdict(env, windows=120):
    return soak.evaluate(child_evidence(windows=windows), load_samples(windows), **GOOD, **soak.verdict_arguments(env))


class EvidenceInputTests(unittest.TestCase):
    def test_every_workflow_input_is_classified_as_neutral_or_diagnostic(self):
        inputs = workflow_dispatch_inputs()
        self.assertIn("duration_minutes", inputs)
        self.assertIn("soak_variant", inputs)
        classified = set(soak.EVIDENCE_NEUTRAL_INPUTS) | set(soak.DIAGNOSTIC_INPUTS)
        self.assertEqual(set(inputs) - classified, set(), "a new workflow input must be classified as neutral or diagnostic in capacity_soak.py")
        self.assertEqual(classified - set(inputs), set(), "a classified input no longer exists in the workflow")
        self.assertEqual(set(soak.EVIDENCE_NEUTRAL_INPUTS) & set(soak.DIAGNOSTIC_INPUTS), set())

    def test_every_diagnostic_input_has_a_non_default_value_in_this_test(self):
        self.assertEqual(set(NON_DEFAULT), set(soak.DIAGNOSTIC_INPUTS))
        for name, value in NON_DEFAULT.items():
            self.assertNotIn(value, soak.DIAGNOSTIC_INPUTS[name][1], name)

    def test_only_the_all_default_postgres_dispatch_run_counts_as_evidence(self):
        verdict = evidence_verdict(evidence_environment())
        self.assertTrue(verdict["passed"], verdict)
        self.assertTrue(verdict["counts_as_ops08_evidence"], verdict["evidence_blockers"])
        self.assertEqual(verdict["evidence_blockers"], [])
        for name, (variable, _defaults) in soak.DIAGNOSTIC_INPUTS.items():
            verdict = evidence_verdict(evidence_environment(**{variable: NON_DEFAULT[name]}))
            self.assertTrue(verdict["passed"], name)
            self.assertFalse(verdict["counts_as_ops08_evidence"], name)
            self.assertTrue(any(name in blocker for blocker in verdict["evidence_blockers"]), (name, verdict["evidence_blockers"]))
        # The diagnosis switch the pull request smoke sets, and every explicit default, are judged too.
        diagnose = evidence_verdict(evidence_environment(APP_CAPACITY_SOAK_DIAGNOSE="1"))
        self.assertFalse(diagnose["counts_as_ops08_evidence"])
        for name, (variable, defaults) in soak.DIAGNOSTIC_INPUTS.items():
            for default in defaults:
                self.assertTrue(evidence_verdict(evidence_environment(**{variable: default}))["counts_as_ops08_evidence"], (name, default))

    def test_a_runner_profile_run_is_not_evidence(self):
        verdict = evidence_verdict(evidence_environment(APP_CAPACITY_SOAK_RUNNER_PROFILE="1", APP_CAPACITY_SOAK_DIAGNOSE="1"))
        self.assertTrue(verdict["passed"])
        self.assertFalse(verdict["counts_as_ops08_evidence"])

    def test_the_workflow_hands_each_diagnostic_input_to_its_variable_and_keeps_it_through_sudo(self):
        text = WORKFLOW.read_text()
        job = text[text.index("\n  capacity-soak:"):]
        preserved = re.search(r"--preserve-env=(\S+)", job).group(1).split(",")
        for name, (variable, _defaults) in soak.DIAGNOSTIC_INPUTS.items():
            self.assertIn(f"inputs.{name}", job_env_line(variable), name)
            self.assertIn(variable, preserved, variable)
        self.assertIn("inputs.expected_commit", job_env_line("APP_CAPACITY_SOAK_EXPECTED_COMMIT"))
        for variable in ("APP_CAPACITY_SOAK_EXPECTED_COMMIT", "APP_CAPACITY_SOAK_DIAGNOSE", "GITHUB_SHA", "GITHUB_EVENT_NAME"):
            self.assertIn(variable, preserved, variable)

    def test_only_a_workflow_dispatch_counts(self):
        for event in ("pull_request", "push", "schedule", ""):
            verdict = evidence_verdict(evidence_environment(GITHUB_EVENT_NAME=event))
            self.assertTrue(verdict["passed"], event)
            self.assertFalse(verdict["counts_as_ops08_evidence"], event)
            self.assertTrue(any("event" in blocker for blocker in verdict["evidence_blockers"]), event)

    def test_the_expected_commit_must_match_when_the_reviewer_names_one(self):
        sha = "a" * 40
        matching = evidence_verdict(evidence_environment(APP_CAPACITY_SOAK_EXPECTED_COMMIT=sha))
        self.assertTrue(matching["counts_as_ops08_evidence"])
        self.assertEqual((matching["commit"], matching["expected_commit"], matching["commit_matches_expected"]), (sha, sha, True))
        other = evidence_verdict(evidence_environment(APP_CAPACITY_SOAK_EXPECTED_COMMIT="b" * 40))
        self.assertFalse(other["counts_as_ops08_evidence"])
        self.assertIs(other["commit_matches_expected"], False)
        self.assertTrue(any("commit" in blocker for blocker in other["evidence_blockers"]))
        unnamed = evidence_verdict(evidence_environment())
        self.assertTrue(unnamed["counts_as_ops08_evidence"])
        self.assertIsNone(unnamed["commit_matches_expected"])
        self.assertEqual(unnamed["commit"], sha)

    def test_the_expected_commit_is_a_full_hexadecimal_sha_or_empty(self):
        self.assertEqual(soak.expected_commit(""), "")
        self.assertEqual(soak.expected_commit("a" * 40), "a" * 40)
        for bad in ("a" * 7, "A" * 40, "g" * 40, " " + "a" * 40, "a" * 41):
            with self.assertRaises(RuntimeError):
                soak.expected_commit(bad)


class FsyncProbeTests(unittest.TestCase):
    def test_the_probe_times_each_synced_write_and_reports_p50_p95_and_the_maximum(self):
        ticks = iter(x for i in range(200) for x in (i * 10.0, i * 10.0 + (0.0010 if i < 190 else 0.0100)))
        syncs = []
        with tempfile.TemporaryDirectory() as root:
            result = soak.fsync_probe(root, writes=200, size=4096, clock=lambda: next(ticks), sync=syncs.append)
            self.assertEqual(list(Path(root).iterdir()), [], "the probe file is removed")
        self.assertEqual(len(syncs), 200)
        self.assertEqual(result["writes"], 200)
        self.assertEqual(result["bytes_per_write"], 4096)
        self.assertAlmostEqual(result["p50_ms"], 1.0, places=3)
        self.assertAlmostEqual(result["p95_ms"], 1.0, places=3)
        self.assertAlmostEqual(result["max_ms"], 10.0, places=3)

    def test_the_probe_removes_its_file_when_a_sync_fails(self):
        def broken(fd):
            raise OSError("I/O error")

        with tempfile.TemporaryDirectory() as root:
            with self.assertRaises(OSError):
                soak.fsync_probe(root, writes=3, sync=broken)
            self.assertEqual(list(Path(root).iterdir()), [])

    def test_the_probe_really_writes_and_syncs_when_nothing_is_injected(self):
        with tempfile.TemporaryDirectory() as root:
            result = soak.fsync_probe(root, writes=5)
        self.assertEqual(result["writes"], 5)
        self.assertGreaterEqual(result["p50_ms"], 0.0)


def wal_sample(at_ms, mean_ms, syncs_per_second=5.0):
    return {"at_ms": at_ms, "pg": {"commits_per_second": 90.0, "mean_wal_sync_ms": mean_ms, "wal_syncs_per_second": syncs_per_second}}


def idle_evidence(idle_start=0, idle_end=180_000):
    evidence = child_evidence(windows=3)
    evidence["idle_windows"] = [{"minute": 0, "started_ms": idle_start, "ended_ms": idle_end}]
    evidence["load_started_ms"] = LOAD_START_MS
    return evidence


FAST_PROBE = {"writes": 200, "bytes_per_write": 4096, "p50_ms": 0.1, "p95_ms": 0.3, "max_ms": 0.9}


class DiskPrecheckTests(unittest.TestCase):
    def test_the_thresholds_are_named_constants(self):
        self.assertEqual(soak.DISK_PRECHECK_WAL_SYNC_MS, 2.0)
        self.assertEqual(soak.DISK_PRECHECK_PROBE_P50_MS, 1.0)
        self.assertEqual(soak.RESULT_INCONCLUSIVE_SLOW_DISK, "inconclusive_slow_disk")

    def test_the_idle_wal_sync_is_the_mean_weighted_by_the_syncs_of_each_interval_inside_the_baseline_only(self):
        evidence = idle_evidence()
        samples = [wal_sample(30_000, 1.0, 10.0), wal_sample(60_000, 4.0, 30.0),
                   wal_sample(200_000, 50.0, 10.0),          # after the baseline: the load, never part of the precheck
                   {"at_ms": 90_000}]                        # no reading: skipped
        self.assertAlmostEqual(soak.idle_wal_sync_ms(evidence, samples), (1.0 * 10 + 4.0 * 30) / 40, places=6)
        self.assertIsNone(soak.idle_wal_sync_ms(evidence, [{"at_ms": 90_000}]))
        self.assertIsNone(soak.idle_wal_sync_ms({}, samples))
        self.assertIsNone(soak.idle_wal_sync_ms(evidence, [wal_sample(30_000, 3.0, 0.0)]))

    def test_the_precheck_trips_on_a_slow_idle_wal_sync_or_a_slow_probe_and_names_the_signal(self):
        ok = soak.disk_precheck(0.06, FAST_PROBE)
        self.assertFalse(ok["tripped"])
        self.assertEqual(ok["reasons"], [])
        wal = soak.disk_precheck(2.5, FAST_PROBE)
        self.assertTrue(wal["tripped"])
        self.assertTrue(any("WAL sync" in r for r in wal["reasons"]))
        probe = soak.disk_precheck(0.06, dict(FAST_PROBE, p50_ms=1.5))
        self.assertTrue(probe["tripped"])
        self.assertTrue(any("fsync probe" in r for r in probe["reasons"]))
        # At the bound is not over it.
        self.assertFalse(soak.disk_precheck(2.0, dict(FAST_PROBE, p50_ms=1.0))["tripped"])
        # A signal that was not measured cannot trip: the other one still can.
        self.assertFalse(soak.disk_precheck(None, None)["tripped"])
        self.assertTrue(soak.disk_precheck(None, dict(FAST_PROBE, p50_ms=3.0))["tripped"])
        self.assertEqual(ok["signals"], {"idle_wal_sync_ms": 0.06, "idle_wal_sync_judged": True, "probe": FAST_PROBE})

    def test_a_slow_postgres_wal_does_not_trip_a_sqlite_run_but_is_recorded(self):
        # Run 43764872093 (SQLite): PostgreSQL's idle WAL sync averaged 7.7 ms, on a database the Portal did not use.
        sqlite = soak.disk_precheck(7.7, FAST_PROBE, "sqlite")
        self.assertFalse(sqlite["tripped"])
        self.assertEqual(sqlite["reasons"], [])
        self.assertEqual(sqlite["signals"]["idle_wal_sync_ms"], 7.7)
        self.assertFalse(sqlite["signals"]["idle_wal_sync_judged"])
        self.assertEqual(sqlite["database"], "sqlite")
        # PostgreSQL runs keep the rule, with the driver named or left to the default.
        for postgres in (soak.disk_precheck(7.7, FAST_PROBE, "postgres"), soak.disk_precheck(7.7, FAST_PROBE)):
            self.assertTrue(postgres["tripped"])
            self.assertTrue(any("WAL sync" in r for r in postgres["reasons"]))
            self.assertTrue(postgres["signals"]["idle_wal_sync_judged"])
            self.assertEqual(postgres["database"], "postgres")

    def test_a_sqlite_run_is_still_judged_on_the_fsync_probe_of_the_device_holding_its_file(self):
        slow = soak.disk_precheck(0.06, dict(FAST_PROBE, p50_ms=1.5), "sqlite")
        self.assertTrue(slow["tripped"])
        self.assertTrue(any("fsync probe" in r for r in slow["reasons"]))
        self.assertFalse(any("WAL sync" in r for r in slow["reasons"]))
        self.assertFalse(soak.disk_precheck(None, dict(FAST_PROBE, p50_ms=1.0), "sqlite")["tripped"])

    def test_the_verdict_of_a_sqlite_run_ignores_a_slow_postgres_wal_and_says_it_is_diagnostic(self):
        evidence = child_evidence(windows=120)
        evidence["db_driver"] = "sqlite"
        evidence["idle_windows"] = [{"minute": 0, "started_ms": 0, "ended_ms": 180_000}]
        samples = load_samples(120) + [wal_sample(30_000, 7.7)]
        verdict = soak.evaluate(evidence, samples, disk_probe=FAST_PROBE, database="sqlite-file", event="workflow_dispatch", **GOOD)
        self.assertEqual(verdict["result"], "passed")
        self.assertTrue(verdict["passed"])
        self.assertFalse(verdict["precheck"]["tripped"])
        self.assertEqual(verdict["precheck"]["signals"]["idle_wal_sync_ms"], 7.7)
        self.assertFalse(verdict["counts_as_ops08_evidence"])
        self.assertTrue(verdict["diagnostic"])
        self.assertTrue(any("not postgres" in b for b in verdict["evidence_blockers"]))
        text = soak.render_summary(evidence, verdict)
        self.assertIn("DIAGNOSTIC", text.splitlines()[0])
        self.assertIn("never OPS-08 evidence", text)
        # The same readings on a PostgreSQL run still end it as inconclusive.
        evidence["db_driver"] = "postgres"
        pg = soak.evaluate(evidence, samples, disk_probe=FAST_PROBE, event="workflow_dispatch", **GOOD)
        self.assertEqual(pg["result"], "inconclusive_slow_disk")
        self.assertFalse(pg.get("diagnostic", False))

    def test_the_fsync_probe_is_taken_where_the_database_the_portal_uses_lives(self):
        # SQLite: the fixture's own directory, whatever it is, and nothing before it exists.
        self.assertEqual(soak.probe_directory("sqlite", "/var/lib/postgresql/16/main", "/run/x/portal-fixture"), "/run/x/portal-fixture")
        self.assertEqual(soak.probe_directory("sqlite-file", "", "/mnt/y/portal-fixture"), "/mnt/y/portal-fixture")
        self.assertIsNone(soak.probe_directory("sqlite", "/var/lib/postgresql/16/main"))
        self.assertNotIn("/tmp", [soak.probe_directory("sqlite", "", "/data/fixture")])
        # PostgreSQL: the parent of the server's data directory; the fixture is irrelevant.
        self.assertEqual(soak.probe_directory("postgres", "/var/lib/postgresql/16/main", "/run/x/portal-fixture"), "/var/lib/postgresql/16")
        self.assertEqual(soak.probe_directory("", "/var/lib/postgresql/16/main"), "/var/lib/postgresql/16")
        self.assertIsNone(soak.probe_directory("postgres", ""))

    def test_the_runner_probes_the_fixture_it_creates_not_a_hard_coded_directory(self):
        source = SOURCE.read_text()
        self.assertNotIn('return "/tmp"', source)
        self.assertIn("probe_directory(database, postgres_data_dir, fixture)", source)

    def test_a_tripped_precheck_ends_the_run_as_inconclusive_never_evidence_neither_passed_nor_failed(self):
        samples = load_samples(120) + [wal_sample(30_000, 3.0)]
        evidence = child_evidence(windows=120)
        evidence["idle_windows"] = [{"minute": 0, "started_ms": 0, "ended_ms": 180_000}]
        verdict = soak.evaluate(evidence, samples, **GOOD, event="workflow_dispatch")
        self.assertEqual(verdict["result"], "inconclusive_slow_disk")
        self.assertIsNone(verdict["passed"])
        self.assertFalse(verdict["counts_as_ops08_evidence"])
        self.assertTrue(verdict["precheck"]["tripped"])
        self.assertTrue(any("disk" in blocker for blocker in verdict["evidence_blockers"]))

    def test_a_slow_probe_alone_makes_the_run_inconclusive_even_when_every_check_failed(self):
        evidence = child_evidence(windows=120)
        evidence["loaded"]["home"]["p95_us"] = 90_000
        verdict = soak.evaluate(evidence, load_samples(120), disk_probe=dict(FAST_PROBE, p50_ms=2.0), event="workflow_dispatch", **GOOD)
        self.assertIn("home_p95_within_absolute_bound", {c["name"] for c in verdict["checks"] if not c["passed"]})
        self.assertEqual(verdict["result"], "inconclusive_slow_disk")
        self.assertIsNone(verdict["passed"])

    def test_a_disk_that_degrades_during_the_load_stays_a_failure(self):
        # The idle baseline is healthy; the load windows have a slow WAL sync and the Portal missed its bound: that is the Portal's FAIL.
        evidence = child_evidence(windows=120)
        evidence["idle_windows"] = [{"minute": 0, "started_ms": 0, "ended_ms": 180_000}]
        evidence["loaded"]["report"]["p95_us"] = 90_000
        loaded = load_samples(120)
        for sample in loaded:
            if sample["at_ms"] > LOAD_START_MS:
                sample["pg"] = {"commits_per_second": 90.0, "mean_wal_sync_ms": 9.0, "wal_syncs_per_second": 40.0}
        samples = [wal_sample(30_000, 0.06)] + loaded
        verdict = soak.evaluate(evidence, samples, disk_probe=FAST_PROBE, event="workflow_dispatch", **GOOD)
        self.assertFalse(verdict["precheck"]["tripped"])
        self.assertEqual(verdict["result"], "failed")
        self.assertFalse(verdict["passed"])

    def test_a_healthy_run_is_passed_and_a_failing_one_failed(self):
        self.assertEqual(soak.evaluate(child_evidence(windows=3), load_samples(3), disk_probe=FAST_PROBE, **GOOD)["result"], "passed")
        broken = child_evidence(windows=3)
        broken["loaded"]["home"]["p95_us"] = 99_000
        self.assertEqual(soak.evaluate(broken, load_samples(3), **GOOD)["result"], "failed")

    def test_the_failed_hosts_idle_readings_are_below_the_wal_threshold_so_the_probe_is_the_signal_that_decides(self):
        # The idle baseline of run 43678283415 attempt 1 (the slow disk that failed home and batch start): six WAL sync means at
        # about five syncs a second. Its weighted mean, 1.31 ms, is under the 2 ms bound: the WAL signal alone would not have flagged it.
        readings = [(0.705, 15.77), (0.642, 5.83), (1.536, 5.1), (1.145, 5.23), (2.247, 5.07), (2.987, 5.1)]
        evidence = idle_evidence()
        samples = [wal_sample(30_000 * (i + 1), mean, rate) for i, (mean, rate) in enumerate(readings)]
        measured = soak.idle_wal_sync_ms(evidence, samples)
        self.assertAlmostEqual(measured, 1.314, places=2)
        self.assertFalse(soak.disk_precheck(measured, None)["tripped"])
        # The green runs of the same configuration measured 0.06, 0.16 and 0.70 ms idle: a probe above 1 ms on that class of disk trips.
        self.assertTrue(soak.disk_precheck(measured, dict(FAST_PROBE, p50_ms=1.3))["tripped"])

    def test_the_step_summary_names_an_inconclusive_run_and_its_reasons_instead_of_calling_it_failed(self):
        evidence = child_evidence(windows=3)
        verdict = soak.evaluate(evidence, load_samples(3), disk_probe=dict(FAST_PROBE, p50_ms=2.0), event="workflow_dispatch", **GOOD)
        text = soak.render_summary(evidence, verdict)
        self.assertIn("INCONCLUSIVE", text.splitlines()[0])
        self.assertNotIn("FAILED", text.splitlines()[0])
        self.assertIn("fsync probe", text)
        self.assertIn("Rerun the job", text)
        passed = soak.render_summary(evidence, soak.evaluate(evidence, load_samples(3), **GOOD))
        self.assertIn(": passed", passed.splitlines()[0])

    def test_the_precheck_alone_ends_a_run_before_the_load_when_the_probe_is_slow(self):
        self.assertTrue(soak.probe_trips(dict(FAST_PROBE, p50_ms=1.01)))
        self.assertFalse(soak.probe_trips(FAST_PROBE))
        self.assertFalse(soak.probe_trips(None))


class JournalSizeTests(unittest.TestCase):
    def test_the_journal_files_are_sized_and_summed_and_other_files_are_ignored(self):
        with tempfile.TemporaryDirectory() as root:
            for name, size in (("journal.sqlite3", 100), ("journal.sqlite3-wal", 40), ("journal.sqlite3-shm", 8), ("state.db", 10), ("x.db-wal", 2),
                               ("notes.txt", 999), ("lock", 5)):
                (Path(root) / name).write_bytes(b"x" * size)
            (Path(root) / "sub.db").mkdir()
            reading = soak.journal_sizes(root)
        self.assertEqual(reading["files"], {"journal.sqlite3": 100, "journal.sqlite3-wal": 40, "journal.sqlite3-shm": 8, "state.db": 10, "x.db-wal": 2})
        self.assertEqual(reading["total"], 160)

    def test_a_missing_directory_is_no_reading(self):
        self.assertIsNone(soak.journal_sizes("/nonexistent-journal-directory"))
        self.assertIsNone(soak.journal_sizes(None))

    def test_the_sample_carries_the_journal_sizes_when_the_sampler_is_given_the_directory(self):
        with tempfile.TemporaryDirectory() as root:
            journal = Path(root) / "control"
            journal.mkdir()
            (journal / "journal.sqlite3").write_bytes(b"x" * 64)
            proc = Path(root) / "proc"
            proc.mkdir()
            (proc / "meminfo").write_text(MEMINFO)
            (proc / "stat").write_text("cpu  100 0 100 700 100 0 0 0 0 0\n")
            (proc / "loadavg").write_text("0.5 0.4 0.3 4/600 99\n")
            mount = Path(root) / "mount"
            mount.mkdir()
            sampler = soak.Sampler(run=lambda *a: "", proc=proc, mountpoint=mount, image_id="sha256:" + "b" * 64, clock=lambda: 1000.0, journal_dir=journal)
            row = sampler.sample()
        self.assertEqual(row["journal_bytes"], {"files": {"journal.sqlite3": 64}, "total": 64})


if __name__ == "__main__":
    unittest.main()
