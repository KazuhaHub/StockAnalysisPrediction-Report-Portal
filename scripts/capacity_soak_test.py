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


def backend_status(index, **changes):
    status = {"installation": f"installation-{index}", "answered": True, "held_bytes": 350 * 1024 ** 2, "threads": 40, "burners": 1,
              "log_bytes": 900000, "storage_bytes": 256 * 1024 ** 2, "storage_full": 2, "data_present": True, "requests": 10}
    status.update(changes)
    return status


def child_evidence(windows=3, home=1000, report=2000, dispatch=300, flood_ok=100):
    window = lambda m, scale: {
        "minute": m, "at_ms": 1000 * m,
        "home": {"count": 100, "p50_us": 1, "p95_us": int(home * scale), "max_us": 5},
        "report": {"count": 100, "p50_us": 1, "p95_us": int(report * scale), "max_us": 5},
        "dispatch": {"count": 100, "p50_us": 1, "p95_us": int(dispatch * scale), "max_us": 5},
        "flood": {"backend_requests": 100, "backend_requests_ok": flood_ok, "backend_request_errors": 100 - flood_ok, "task_refusals": 5},
        "backends": [backend_status(i) for i in range(8)],
    }
    return {
        "format": "portal.capacity-soak.child/1-dev.1",
        "budgets": {"memory_mib": 3993, "cpu_millis": 2000, "disk_bytes": 8 << 30, "image_store_bytes": 10 << 30, "running_backends": 8},
        "sizing": {"MemoryMiB": 497, "CPUMillis": 246},
        "probes": [{"name": name, "expected": "x", "observed": "x", "passed": True, "native": True,
                    **({"retired": "managed_uninstall"} if name in soak.RETIRED_PROBES else {})} for name in soak.REQUIRED_PROBES]
                  + [{"name": "task_over_application_quota", "expected": "y", "observed": "y", "passed": True, "native": False}],
        "idle": window(0, 1.0),
        "windows": [window(m, 1.2) for m in range(1, windows + 1)],
        "load_started_ms": 500, "load_finished_ms": 1000 * (windows + 1), "duration_minutes": windows,
        "markers": ["load_finished"],
    }


def backend_sample(at_ms, mem_mib=3900, cpu=1900.0, running=8, total=8, oom=0, restarts=0):
    return {"at_ms": at_ms, "mem_total_kib": 1, "mem_available_kib": 1,
            "backends": {"total": total, "running": running, "oom_killed": oom, "restarting": 0, "exited": total - running,
                         "restart_count": restarts, "memory_bytes": mem_mib * 1024 ** 2, "cpu_millis": cpu, "pids": 400}}


class EvaluateTests(unittest.TestCase):
    def verdict(self, evidence=None, samples=None, minutes=3, exit_ok=True):
        evidence = evidence or child_evidence(minutes)
        samples = samples if samples is not None else [backend_sample(1000 * m + 600) for m in range(1, minutes + 1)]
        return soak.evaluate(evidence, samples, parent_passed=exit_ok)

    def failing(self, verdict):
        return {c["name"] for c in verdict["checks"] if not c["passed"]}

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

    def test_p95_at_the_threshold_passes_and_one_microsecond_beyond_fails(self):
        for idle in (1000, 40000):
            threshold = soak.latency_threshold_us(idle)
            evidence = child_evidence(home=idle)
            for window in evidence["windows"]:
                window["home"]["p95_us"] = threshold
            self.assertTrue(self.verdict(evidence)["passed"], idle)
            evidence["windows"][1]["home"]["p95_us"] = threshold + 1
            self.assertIn("home_p95_within_threshold_of_idle", self.failing(self.verdict(evidence)))

    def test_each_latency_series_is_checked_on_its_own(self):
        for series, check in (("report", "report_p95_within_threshold_of_idle"), ("dispatch", "dispatch_within_threshold_of_idle")):
            evidence = child_evidence()
            evidence["windows"][0][series]["p95_us"] = soak.latency_threshold_us(evidence["idle"][series]["p95_us"]) + 1
            self.assertEqual(self.failing(self.verdict(evidence)), {check})

    def test_the_effective_threshold_of_every_window_is_recorded(self):
        verdict = self.verdict()
        check = next(c for c in verdict["checks"] if c["name"] == "home_p95_within_threshold_of_idle")
        rows = check["detail"]["window_thresholds"]
        self.assertEqual(len(rows), 3)
        self.assertEqual(rows[0], {"minute": 1, "p95_us": 1200, "threshold_us": 6000, "passed": True})

    def test_summed_backend_memory_and_cpu_must_stay_inside_the_budgets(self):
        over_memory = [backend_sample(1600, mem_mib=3994)]
        self.assertEqual(self.failing(self.verdict(samples=over_memory)), {"backend_memory_within_budget"})
        over_cpu = [backend_sample(1600, cpu=2000.5)]
        self.assertEqual(self.failing(self.verdict(samples=over_cpu)), {"backend_cpu_within_budget"})
        exact = [backend_sample(1600, mem_mib=3993, cpu=2000.0)]
        self.assertTrue(self.verdict(samples=exact)["passed"])

    def test_samples_outside_the_load_do_not_count(self):
        before_load = [backend_sample(100, mem_mib=9999, cpu=9999.0), backend_sample(1600)]
        self.assertTrue(self.verdict(samples=before_load)["passed"])

    def test_a_missing_exited_oom_or_restarted_backend_is_a_crash(self):
        for sample in (backend_sample(1600, running=7), backend_sample(1600, oom=1), backend_sample(1600, restarts=1)):
            self.assertIn("no_backend_crash", self.failing(self.verdict(samples=[sample])))

    def test_no_samples_during_the_load_is_a_failure_not_a_pass(self):
        self.assertIn("host_samples_cover_the_load", self.failing(self.verdict(samples=[])))

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

    def test_an_unrefused_probe_fails_and_unit_level_probes_are_listed(self):
        evidence = child_evidence()
        evidence["probes"][0].update(observed="capacity_exhausted_disk", passed=False)
        verdict = self.verdict(evidence)
        self.assertIn("every_excess_request_refused_with_its_typed_error", self.failing(verdict))
        self.assertEqual(verdict["unit_level_probes"], ["task_over_application_quota"])

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

    def test_every_backend_must_show_it_ran_each_of_its_loads(self):
        for change in ({"answered": False}, {"held_bytes": 100 * 1024 ** 2}, {"threads": 3}, {"burners": 0}, {"log_bytes": 0},
                       {"storage_full": 0}, {"data_present": False}):
            evidence = child_evidence()
            for window in evidence["windows"]:
                window["backends"][3] = backend_status(3, **change)
            self.assertEqual(self.failing(self.verdict(evidence)), {"backends_ran_every_declared_load"}, change)
        evidence = child_evidence()
        for window in evidence["windows"]:
            window["backends"] = window["backends"][:7]
        self.assertIn("backends_ran_every_declared_load", self.failing(self.verdict(evidence)))
        evidence = child_evidence()
        for window in evidence["windows"]:
            del window["backends"]
        self.assertIn("backends_ran_every_declared_load", self.failing(self.verdict(evidence)))

    def test_a_backend_that_missed_only_the_last_status_request_is_judged_on_its_earlier_answer(self):
        evidence = child_evidence()
        evidence["windows"][-1]["backends"][2] = {"installation": "installation-2", "answered": False}
        self.assertTrue(self.verdict(evidence)["passed"])
        for window in evidence["windows"]:
            window["backends"][2] = {"installation": "installation-2", "answered": False}
        self.assertIn("backends_ran_every_declared_load", self.failing(self.verdict(evidence)))

    def test_cpu_is_judged_on_cgroup_samples_when_there_are_any(self):
        rate_only = backend_sample(1600, cpu=2020.0)
        rate_only["backends"]["cpu_source"] = "docker_stats"
        exact = backend_sample(1700, cpu=1995.0)
        exact["backends"]["cpu_source"] = "cgroup"
        self.assertTrue(self.verdict(samples=[rate_only, exact])["passed"])
        self.assertIn("backend_cpu_within_budget", self.failing(self.verdict(samples=[rate_only])))

    def test_flood_that_never_reached_a_backend_fails(self):
        evidence = child_evidence(flood_ok=0)
        self.assertIn("backends_answered_the_message_flood", self.failing(self.verdict(evidence)))

    def test_ops08_evidence_requires_the_full_duration_and_a_pass(self):
        smoke = self.verdict(minutes=5)
        self.assertTrue(smoke["passed"])
        self.assertFalse(smoke["counts_as_ops08_evidence"])
        evidence = child_evidence(windows=120)
        samples = [backend_sample(1000 * m + 600) for m in range(1, 121)]
        full = soak.evaluate(evidence, samples, parent_passed=True)
        self.assertTrue(full["passed"], full)
        self.assertTrue(full["counts_as_ops08_evidence"])


class ConfigurationTests(unittest.TestCase):
    def test_image_must_be_an_owned_loopback_digest(self):
        self.assertEqual(soak.soak_image(IMAGE), IMAGE)
        for bad in ["", "registry.example/app@sha256:" + "a" * 64, "localhost:0/portal-soak-backend@sha256:" + "a" * 64,
                    "localhost:5000/portal-ready-fixture@sha256:" + "a" * 64, "localhost:5000/portal-soak-backend:latest"]:
            with self.assertRaises(RuntimeError):
                soak.soak_image(bad)

    def test_duration_is_bounded(self):
        self.assertEqual(soak.duration_minutes("120"), 120)
        for bad in ["0", "-1", "x", "", "181", "3.5"]:
            with self.assertRaises(RuntimeError):
                soak.duration_minutes(bad)

    def test_baseline_scales_with_the_run(self):
        self.assertEqual(soak.baseline_seconds(5), 30)
        self.assertEqual(soak.baseline_seconds(120), 180)

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
