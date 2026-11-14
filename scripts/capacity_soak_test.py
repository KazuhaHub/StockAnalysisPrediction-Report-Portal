import importlib.util
import json
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


def child_evidence(windows=3, home=1000, report=2000, dispatch=300, batch=400, flood_ok=100):
    def window(m, scale):
        return {
            "minute": m, "at_ms": LOAD_START_MS + PERIOD_MS * m, "started_ms": LOAD_START_MS + PERIOD_MS * (m - 1),
            "ended_ms": LOAD_START_MS + PERIOD_MS * m,
            "home": stats(int(home * scale)), "report": stats(int(report * scale)), "dispatch": stats(int(dispatch * scale)),
            "batch_start": stats(int(batch * scale), 20),
            "flood": flood(answered=flood_ok),
            "backends": [backend_status(i, window=m) for i in range(8)],
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
    }


CPU_QUOTA_MILLIS = 246
PIDS_MAX = 64


def per_backend(index, at_ms, usage_usec, mem_fraction=0.9, pids=58, quota=CPU_QUOTA_MILLIS):
    return {"id": f"container-{index}", "usage_usec": usage_usec, "memory_current": int(mem_fraction * LIMIT_MIB * 1024 ** 2),
            "memory_max": LIMIT_MIB * 1024 ** 2, "cpu_quota_millis": quota, "pids_current": pids, "pids_max": PIDS_MAX}


def backend_sample(at_ms, mem_mib=3900, cpu=1900.0, running=8, total=8, oom=0, restarts=0, usage_fraction=0.95, mem_fraction=0.9, pids=58,
                   available_kib=12_000_000, detail=True):
    """One host sample. usage_usec counts from LOAD_START_MS so a backend at usage_fraction burns that share of its quota."""
    seconds = (at_ms - LOAD_START_MS) / 1000.0
    usage = int(max(seconds, 0) * CPU_QUOTA_MILLIS * usage_fraction * 1000.0)
    sample = {"at_ms": at_ms, "mem_total_kib": 16_380_000, "mem_available_kib": available_kib,
              "backends": {"total": total, "running": running, "oom_killed": oom, "restarting": 0, "exited": total - running,
                           "restart_count": restarts, "memory_bytes": mem_mib * 1024 ** 2, "cpu_millis": cpu, "pids": 400}}
    if detail:
        sample["backends"]["per_backend"] = [per_backend(i, at_ms, usage, mem_fraction=mem_fraction, pids=pids) for i in range(8)]
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
        return soak.evaluate(evidence, samples, parent_passed=exit_ok, **kwargs)

    def failing(self, verdict):
        return {c["name"] for c in verdict["checks"] if not c["passed"]}

    def check(self, verdict, name):
        return next(c for c in verdict["checks"] if c["name"] == name)

    def test_a_clean_run_passes(self):
        verdict = self.verdict()
        self.assertTrue(verdict["passed"], verdict)
        self.assertEqual(self.failing(verdict), set())

    def test_the_threshold_is_the_larger_of_one_and_a_half_times_and_five_milliseconds_more(self):
        self.assertEqual(soak.LATENCY_FLOOR_US, 5000)
        self.assertEqual(soak.latency_threshold_us(1000), 6000)      # the floor governs a sub-millisecond baseline
        self.assertEqual(soak.latency_threshold_us(10000), 15000)    # the two rules meet at 10 ms
        self.assertEqual(soak.latency_threshold_us(40000), 60000)    # the ratio governs a slow baseline
        self.assertEqual(soak.latency_threshold_us(0), 5000)         # an unmeasured baseline is not a free pass

    def test_the_pooled_loaded_p95_at_the_threshold_passes_and_one_microsecond_beyond_fails(self):
        for idle in (1000, 40000):
            threshold = soak.latency_threshold_us(idle)
            evidence = child_evidence(home=idle)
            evidence["loaded"]["home"]["p95_us"] = threshold
            self.assertTrue(self.verdict(evidence)["passed"], idle)
            evidence["loaded"]["home"]["p95_us"] = threshold + 1
            self.assertEqual(self.failing(self.verdict(evidence)), {"home_p95_within_threshold_of_idle"})

    def test_each_latency_series_is_judged_on_its_own_pooled_p95(self):
        for series, check in (("report", "report_p95_within_threshold_of_idle"), ("dispatch", "dispatch_within_threshold_of_idle"),
                              ("batch_start", "batch_start_within_threshold_of_idle")):
            evidence = child_evidence()
            evidence["loaded"][series]["p95_us"] = soak.latency_threshold_us(evidence["idle"][series]["p95_us"]) + 1
            self.assertEqual(self.failing(self.verdict(evidence)), {check})

    def test_a_pooled_pass_still_lists_the_windows_over_the_threshold_as_diagnostics(self):
        evidence = child_evidence(windows=4)
        threshold = soak.latency_threshold_us(evidence["idle"]["home"]["p95_us"])
        evidence["windows"][1]["home"]["p95_us"] = threshold + 4000
        evidence["windows"][3]["home"]["p95_us"] = threshold + 1
        verdict = self.verdict(evidence, minutes=4)
        self.assertTrue(verdict["passed"], verdict)
        detail = self.check(verdict, "home_p95_within_threshold_of_idle")["detail"]
        self.assertEqual(detail["windows_over_the_threshold"], [2, 4])
        self.assertEqual(detail["windows_over_count"], 2)
        self.assertEqual(detail["worst_window"], {"minute": 2, "p95_us": threshold + 4000})
        self.assertEqual(len(detail["window_thresholds"]), 4)
        self.assertEqual(detail["pooled_p95_us"], evidence["loaded"]["home"]["p95_us"])

    def test_a_pooled_failure_is_a_failure_even_when_every_window_is_inside(self):
        evidence = child_evidence()
        evidence["loaded"]["report"]["p95_us"] = soak.latency_threshold_us(evidence["idle"]["report"]["p95_us"]) + 1
        verdict = self.verdict(evidence)
        detail = self.check(verdict, "report_p95_within_threshold_of_idle")["detail"]
        self.assertEqual(detail["windows_over_count"], 0)
        self.assertIn("report_p95_within_threshold_of_idle", self.failing(verdict))

    def test_no_pooled_loaded_sample_is_not_a_pass(self):
        for loaded in ({}, {"home": {"count": 0, "p95_us": 0}}):
            evidence = child_evidence()
            evidence["loaded"] = loaded
            self.assertIn("home_p95_within_threshold_of_idle", self.failing(self.verdict(evidence)))

    def test_the_step_summary_lists_every_window_of_every_series(self):
        evidence = child_evidence(windows=3)
        verdict = self.verdict(evidence, minutes=3)
        text = soak.render_summary(evidence, verdict)
        for series in ("home", "report", "dispatch", "batch_start"):
            self.assertIn(f"### {series}", text)
        self.assertEqual(text.count("| 1 |"), 4)  # minute 1 appears once in each series table
        self.assertEqual(text.count("| 3 |"), 4)
        self.assertIn("pooled", text)

    def test_the_effective_threshold_of_every_window_is_recorded(self):
        verdict = self.verdict()
        rows = self.check(verdict, "home_p95_within_threshold_of_idle")["detail"]["window_thresholds"]
        self.assertEqual(len(rows), 3)
        self.assertEqual(rows[0], {"minute": 1, "p95_us": 1200, "threshold_us": 6000, "passed": True})

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
        self.assertEqual(self.failing(verdict), {"backends_held_their_limits_in_every_window"})
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
        self.assertIn("backends_held_their_limits_in_every_window", self.failing(verdict))
        problems = self.check(verdict, "backends_held_their_limits_in_every_window")["detail"]["problems"]
        self.assertTrue(any(p["window"] == 2 and p["backend"] == "container-5" for p in problems), problems)

    def test_memory_current_must_stay_at_or_above_the_held_fraction_in_every_window(self):
        samples = load_samples(3)
        samples[3]["backends"]["per_backend"][0]["memory_current"] = int(0.50 * LIMIT_MIB * 1024 ** 2)
        verdict = self.verdict(samples=samples)
        self.assertIn("backends_held_their_limits_in_every_window", self.failing(verdict))
        problems = self.check(verdict, "backends_held_their_limits_in_every_window")["detail"]["problems"]
        self.assertTrue(any("memory" in p["reason"] for p in problems), problems)

    def test_cgroup_counters_missing_from_the_samples_fail_instead_of_passing_vacuously(self):
        samples = load_samples(3, detail=False)
        self.assertIn("backends_held_their_limits_in_every_window", self.failing(self.verdict(samples=samples)))
        self.assertIn("backends_reached_their_pid_limit", self.failing(self.verdict(samples=samples)))

    def test_pids_must_reach_the_limit_minus_a_small_margin_and_the_fractions_are_recorded(self):
        self.assertEqual(soak.PIDS_REACHED_FRACTION, 0.85)
        low = load_samples(3, pids=40)  # 40 of 64: the first smoke
        verdict = self.verdict(samples=low)
        self.assertEqual(self.failing(verdict), {"backends_reached_their_pid_limit"})
        achieved = self.verdict()["achieved_fractions"]
        self.assertAlmostEqual(achieved["pids_peak"]["min"], 58 / 64, places=3)
        self.assertAlmostEqual(achieved["memory_current_min"]["min"], 0.9, places=3)
        self.assertGreaterEqual(achieved["cpu_window_min"]["min"], 0.94)
        self.assertEqual(len(achieved["pids_peak"]["per_backend"]), 8)

    def test_ops08_evidence_requires_the_full_duration_and_a_pass(self):
        smoke = self.verdict(minutes=5)
        self.assertTrue(smoke["passed"])
        self.assertFalse(smoke["counts_as_ops08_evidence"])
        evidence = child_evidence(windows=120)
        full = soak.evaluate(evidence, load_samples(120), parent_passed=True, unit_tests={"TestTaskQuotasRefuseAtTheirOwnBoundary": True})
        self.assertTrue(full["passed"], self.failing(full))
        self.assertTrue(full["counts_as_ops08_evidence"])


class VariantTests(unittest.TestCase):
    def test_a_diagnostic_variant_never_counts_as_ops08_evidence_even_when_it_passes_for_two_hours(self):
        evidence = child_evidence(windows=120)
        samples = load_samples(120)
        kwargs = {"parent_passed": True, "unit_tests": {"TestTaskQuotasRefuseAtTheirOwnBoundary": True}}
        self.assertTrue(soak.evaluate(evidence, samples, **kwargs)["counts_as_ops08_evidence"])
        verdict = soak.evaluate(evidence, samples, variant="memory-db", **kwargs)
        self.assertTrue(verdict["passed"])
        self.assertFalse(verdict["counts_as_ops08_evidence"])
        self.assertEqual(verdict["variant"], "memory-db")

    def test_the_variant_is_a_closed_list(self):
        self.assertEqual(soak.soak_variant(""), "")
        self.assertEqual(soak.soak_variant("memory-db,no-batch-probes"), "memory-db,no-batch-probes")
        self.assertEqual(soak.soak_variant("skip-image-store"), "skip-image-store")
        for bad in ("memory", "no-batch-probes;x", "a b"):
            with self.assertRaises(RuntimeError):
                soak.soak_variant(bad)


class DatabaseTests(unittest.TestCase):
    def kwargs(self):
        return {"parent_passed": True, "unit_tests": {"TestTaskQuotasRefuseAtTheirOwnBoundary": True}}

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
    return {"parent_passed": True, "unit_tests": {"TestTaskQuotasRefuseAtTheirOwnBoundary": True}}


class CPUBudgetTests(unittest.TestCase):
    def kwargs(self):
        return {"parent_passed": True, "unit_tests": {"TestTaskQuotasRefuseAtTheirOwnBoundary": True}}

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


if __name__ == "__main__":
    unittest.main()
