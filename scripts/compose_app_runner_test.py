"""Lint of the app-runner service in docker-compose.yml (doc 187 sections 6 and 11).

The runner joins the host's mount namespace through its image entry, so every setting here is load-bearing: a
volume would trip the runner's own overlap guard, a cpuset or CPU limit would make the CPU reservation refuse, and
dropping pid: host or network_mode: host makes the entry refuse. These checks read the file as text, so they need
no YAML library and run wherever the unit tests do.
"""
import os
import re
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))


def compose():
    with open(os.path.join(HERE, '..', 'docker-compose.yml')) as f:
        return f.read()


def service(text, name):
    """The block of one service: its lines up to the next two-space key (or the top-level volumes)."""
    body = text.split('\nservices:\n', 1)[1]
    match = re.search(rf'^  {re.escape(name)}:[ \t]*\n(.*?)(?=^  [A-Za-z0-9_-]+:[ \t]*$|^[A-Za-z]|\Z)', body, re.M | re.S)
    if not match:
        raise AssertionError(f'no service {name!r}')
    # Comments are explanations, not settings.
    return '\n'.join(line for line in match[1].splitlines() if not line.lstrip().startswith('#'))


class RunnerServiceTests(unittest.TestCase):
    def setUp(self):
        self.runner = service(compose(), 'app-runner')

    def test_namespaces_and_privilege_are_those_of_the_systemd_unit(self):
        for setting in ('privileged: true', 'network_mode: host', 'pid: host', 'ipc: host', 'uts: host', 'cgroup: host', 'userns_mode: host'):
            self.assertRegex(self.runner, rf'(?m)^    {re.escape(setting)}\s*$', setting)

    def test_open_files_match_the_unit(self):
        self.assertRegex(self.runner, r'nofile:\s*\{\s*soft:\s*65536,\s*hard:\s*65536\s*\}')

    def test_no_mounts_ports_devices_or_resource_limits(self):
        # The data and IPC roots must never be bound (requireNoContainerMountReferences), a cpuset or CPU limit
        # makes reserved_cpus refuse (service/capacity.go), and the runner needs the host's real CPU and memory.
        for forbidden in ('volumes', 'ports', 'devices', 'cpus', 'cpuset', 'cpu_shares', 'cpu_quota', 'cpu_period', 'mem_limit', 'mem_reservation',
                          'memswap_limit', 'deploy', 'pids_limit', 'cap_add', 'cap_drop', 'security_opt', 'tmpfs', 'user', 'depends_on', 'sysctls'):
            self.assertNotRegex(self.runner, rf'(?m)^    {forbidden}\s*:', forbidden)
        self.assertNotRegex(self.runner, r'(?i)\bmemory\b')

    def test_profile_keeps_static_installs_unchanged(self):
        self.assertRegex(self.runner, r'(?m)^    profiles:\s*\["app-runner"\]\s*$')

    def test_the_command_serves_the_documented_config(self):
        self.assertIn('command: ["serve", "--config", "/etc/portal-app-runner/service.json"]', self.runner)
        self.assertIn('stop_signal: SIGTERM', self.runner)
        self.assertRegex(self.runner, r'stop_grace_period:\s*30s')
        self.assertIn('restart: unless-stopped', self.runner)

    def test_healthcheck_runs_health_from_the_host_namespace(self):
        # `docker exec` joins the mount namespace of the container's init, which is the runner in the host's: the
        # image's entry is not there to run, so the check goes through the runner's own /proc/<pid>/exe.
        self.assertIn('- CMD-SHELL', self.runner)
        self.assertIn('cgroup.procs', self.runner)
        self.assertIn('exec "/proc/$$p/exe" health --config /etc/portal-app-runner/service.json', self.runner)
        self.assertNotIn('"/usr/local/bin/portal-app-runner-entry", "health"', self.runner)
        self.assertRegex(self.runner, r'start_period:\s*60s')

    def test_image_follows_the_one_version_variable(self):
        self.assertIn('image: ghcr.io/kazuhahub/stockanalysisprediction-report-portal-app-runner:${PORTAL_VERSION:-latest}', self.runner)
        portal = service(compose(), 'report-portal')
        self.assertIn('image: ghcr.io/kazuhahub/stockanalysisprediction-report-portal:${PORTAL_VERSION:-latest}', portal)
        self.assertNotRegex(portal, r'portal:latest\s*$')


class PortalServiceTests(unittest.TestCase):
    def setUp(self):
        self.portal = service(compose(), 'report-portal')

    def test_control_directory_is_a_read_only_bind_that_docker_never_creates(self):
        self.assertIn('- type: bind', self.portal)
        self.assertIn('source: ${RP_APP_RUNNER_CONTROL_DIR:-./app-runner-control}', self.portal)
        self.assertRegex(self.portal, r'target: /var/lib/portal-app-runner/control\n\s+read_only: true\n\s+bind:\n\s+create_host_path: false')

    def test_portal_gains_nothing_but_that_directory(self):
        # No Docker socket, data root, IPC root or journal in Portal (handbook section 4 step 4).
        for forbidden in ('docker.sock', '/srv/portal', '/run/portal-app-runner', '/var/lib/portal-app-runner/journal', 'privileged', 'pid: host', 'network_mode'):
            self.assertNotIn(forbidden, self.portal, forbidden)

    def test_managed_execution_is_off_by_default(self):
        for name in ('RP_APP_RUNNER_SOCKET', 'RP_APP_RUNNER_CONTROLLER_UID', 'RP_APP_RUNNER_CONTROLLER_GID'):
            self.assertIn(f'{name}: ${{{name}:-}}', self.portal)
        self.assertIn('PUID: ${PORTAL_UID:-10001}', self.portal)
        self.assertIn('PGID: ${PORTAL_GID:-10001}', self.portal)

    def test_header_makes_the_control_directory_for_static_installs(self):
        self.assertIn('mkdir -p app-runner-control', compose().split('\nservices:\n', 1)[0])


if __name__ == '__main__':
    unittest.main()
