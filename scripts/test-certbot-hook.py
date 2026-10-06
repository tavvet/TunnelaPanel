#!/usr/bin/env python3
"""Run the real Certbot hook against isolated mocks; no Docker or services used."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


HOOK = Path(__file__).resolve().parents[1] / "deploy/certbot-deploy-hook.sh"
LINEAGE = "/etc/letsencrypt/live/test.example"

# PATH contains only these mocks, so a broken test cannot invoke real services.
MOCK = r'''
import json, os, pathlib, sys

args = sys.argv[1:]
tool = pathlib.Path(sys.argv[0]).name
if tool == "docker":
    prefix = ["compose", "-p", "tunnelapanel", "-f", os.environ["MOCK_COMPOSE"]]
    if args[:5] != prefix:
        raise SystemExit("Unexpected Compose invocation: " + repr(args))
    command = tuple(args[5:])
    events = {
        ("ps", "--status", "running", "--quiet", "endpoint"): "inspect_endpoint",
        ("--profile", "https", "ps", "--status", "running", "--quiet", "https"): "inspect_https",
        ("kill", "--signal", "SIGHUP", "endpoint"): "reload_endpoint",
        ("--profile", "https", "exec", "-T", "https", "nginx", "-t"): "check_nginx",
        ("--profile", "https", "kill", "--signal", "SIGHUP", "https"): "reload_nginx",
    }
    event = events.get(command)
elif tool == "systemctl" and args == ["try-restart", "trusttunnel"]:
    event = "restart_native"
else:
    event = None
if event is None:
    raise SystemExit("Unexpected service command: " + repr([tool] + args))
with open(os.environ["MOCK_LOG"], "a") as log:
    log.write(json.dumps(event) + "\n")
behavior = json.loads(os.environ["MOCK_BEHAVIOR"]).get(event, {})
sys.stdout.write(behavior.get("stdout", ""))
sys.stderr.write(behavior.get("stderr", ""))
raise SystemExit(behavior.get("status", 0))
'''


class CertbotHookTests(unittest.TestCase):
    def run_hook(self, overrides=None, *, lineage=LINEAGE, docker=True, compose=True):
        behavior = {
            "inspect_endpoint": {"stdout": "endpoint-id\n"},
            "inspect_https": {"stdout": "https-id\n"},
        }
        behavior.update(overrides or {})
        with tempfile.TemporaryDirectory(prefix="certbot-hook-") as directory:
            root = Path(directory)
            bindir = root / "bin"
            bindir.mkdir()
            for tool in ["systemctl"] + (["docker"] if docker else []):
                path = bindir / tool
                path.write_text("#!" + sys.executable + "\n" + MOCK)
                path.chmod(0o700)
            panel = root / "panel with spaces"
            compose_file = panel / "deploy/compose.yml"
            if compose:
                compose_file.parent.mkdir(parents=True)
                compose_file.write_text("# fixture, never read by real Docker\n")
            log = root / "commands.jsonl"
            env = dict(os.environ, PATH=str(bindir), TT_CERT_LINEAGE=LINEAGE,
                       TT_PANEL_DIR=str(panel), MOCK_COMPOSE=str(compose_file),
                       MOCK_LOG=str(log), MOCK_BEHAVIOR=json.dumps(behavior))
            env.pop("RENEWED_LINEAGE", None)
            if lineage is not None:
                env["RENEWED_LINEAGE"] = lineage
            result = subprocess.run(["/bin/sh", str(HOOK)], env=env,
                                    capture_output=True, text=True, timeout=10)
            events = [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []
            return result, events

    def test_reloads_both_consumers(self):
        result, events = self.run_hook()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(events, ["inspect_endpoint", "inspect_https", "reload_endpoint",
                                  "check_nginx", "reload_nginx"])

    def test_stopped_endpoint_uses_native_try_restart(self):
        result, events = self.run_hook({"inspect_endpoint": {}, "inspect_https": {}})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(events, ["inspect_endpoint", "inspect_https", "restart_native"])

    def test_native_fallback_without_docker_or_compose(self):
        for options in ({"docker": False}, {"compose": False}):
            with self.subTest(**options):
                result, events = self.run_hook(**options)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(events, ["restart_native"])

    def test_unrelated_lineage_touches_nothing(self):
        result, events = self.run_hook(lineage="/etc/letsencrypt/live/unrelated.example")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(events, [])

    def test_unset_lineage_permits_manual_smoke_test(self):
        result, events = self.run_hook(lineage=None)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("reload_endpoint", events)
        self.assertIn("reload_nginx", events)

    def test_nginx_validation_failure_does_not_skip_endpoint(self):
        result, events = self.run_hook({"check_nginx": {"status": 1, "stderr": "invalid nginx certificate\n"}})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("invalid nginx certificate", result.stderr)
        self.assertIn("reload_endpoint", events)
        self.assertIn("check_nginx", events)
        self.assertNotIn("reload_nginx", events)

    def test_endpoint_reload_failure_does_not_skip_nginx(self):
        result, events = self.run_hook({"reload_endpoint": {"status": 1, "stderr": "endpoint HUP failed\n"}})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("endpoint HUP failed", result.stderr)
        self.assertIn("check_nginx", events)
        self.assertIn("reload_nginx", events)
        self.assertNotIn("restart_native", events)

    def test_docker_inspection_failure_is_visible(self):
        for event in ("inspect_endpoint", "inspect_https"):
            with self.subTest(event=event):
                result, events = self.run_hook({event: {"status": 42, "stderr": "Docker inspection failed\n"}})
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("Docker inspection failed", result.stderr)
                self.assertIn(event, events)
                self.assertFalse(set(events) & {"restart_native", "reload_endpoint", "reload_nginx"})


if __name__ == "__main__":
    unittest.main(verbosity=2)
