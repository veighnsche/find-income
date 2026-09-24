#!/usr/bin/env python3
"""T22 packaging/probe self-tests. Isolated only: TempDir + loopback.

What runs here must pass on the build machine. Linux-only behavior (bwrap
namespaces, systemd units, live App Server turns) is specified for T29, not
executed: the suit asserts gates, parsers, pure probe logic and fixtures,
and fails closed wherever the host cannot run a stage.
"""
import ast
import importlib.util
import os
import re
import sqlite3
import subprocess
import sys
import tempfile
import tomllib
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]


def load_module(name):
    spec = importlib.util.spec_from_file_location(name, HERE / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def go_tool_names():
    source = (REPO / "apps/api/internal/codexservice/tools.go").read_text()
    match = re.search(r"var requiredTools = \[\]string\{([^}]+)\}", source)
    legacy = set(re.findall(r'"([a-z_]+)"', match.group(1)))
    research = (REPO / "apps/api/internal/codexservice/research_tools.go").read_text()
    manifest = set(re.findall(r'ResearchTool\w+\s+=\s+"([a-z_]+)"', research))
    return legacy, manifest


class TestManifest(unittest.TestCase):
    def test_seventeen_tools_agree(self):
        legacy, manifest = go_tool_names()
        self.assertEqual(len(legacy), 10)
        self.assertEqual(len(manifest), 7)
        want = legacy | manifest
        template = tomllib.loads((HERE / "runner-config.toml.template").read_text())
        self.assertEqual(set(template["mcp_servers"]["jobseek"]["enabled_tools"]), want)
        for probe in ("accept-live-runner.py", "accept-research-readiness.py"):
            tree = ast.parse((HERE / probe).read_text())
            found = None
            for node in tree.body:
                if isinstance(node, ast.Assign) and any(
                        isinstance(t, ast.Name) and t.id == "REQUIRED_TOOLS" for t in node.targets):
                    found = set(ast.literal_eval(node.value))
            self.assertEqual(found, want, probe)

    def test_drift_gate_passes(self):
        proc = subprocess.run([sys.executable, str(HERE / "check-runtime-tools.py")],
                              stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=120)
        self.assertEqual(proc.returncode, 0, proc.stdout.decode())


def canned_mcp_page(tools):
    return {"data": [{"name": "jobseek",
                      "tools": {name: {"name": name} for name in tools}}]}


def canned_config(**overrides):
    config = {"default_permissions": "jobseek-native", "approval_policy": "never",
              "features": dict.fromkeys(("shell_tool", "unified_exec", "view_image", "multi_agent",
                                          "browser_use", "computer_use", "apps", "plugins", "goals"), False)}
    config.update(overrides)
    return config


class StubCall:
    def __init__(self, mcp_tools, config=None, account="chatgpt", models=None):
        self.mcp_tools = mcp_tools
        self.config = canned_config() if config is None else config
        self.account = account
        self.models = models if models is not None else [
            {"model": "m", "supportedReasoningEfforts": [{"reasoningEffort": "e"}]}]
        self.methods = []

    def __call__(self, method, params=None, seconds=20):
        self.methods.append(method)
        if method == "initialize":
            return {"userAgent": "ua", "codexHome": "/state"}
        if method == "account/read":
            return {"account": {"type": self.account}}
        if method == "config/read":
            return {"config": self.config}
        if method == "mcpServerStatus/list":
            return canned_mcp_page(self.mcp_tools)
        if method == "account/rateLimits/read":
            return {}
        if method == "model/list":
            return {"data": self.models}
        raise AssertionError("unexpected method " + method)


class TestReadiness(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.probe = load_module("accept-research-readiness")

    def test_full_surface_passes_without_turns(self):
        _, manifest = go_tool_names()
        legacy, _ = go_tool_names()
        stub = StubCall(legacy | manifest)
        info = self.probe.run_probe(stub, "m", "e")
        self.assertEqual(info["userAgent"], "ua")
        for forbidden in ("thread/start", "turn/start", "thread/turns/list", "thread/read"):
            self.assertNotIn(forbidden, stub.methods)

    def test_missing_research_subset_named(self):
        legacy, _ = go_tool_names()
        stub = StubCall(set(legacy))
        with self.assertRaisesRegex(RuntimeError, "research_execute"):
            self.probe.run_probe(stub)

    def test_missing_server(self):
        stub = StubCall(set())
        stub.mcp_tools = None
        orig = stub.__call__

        def patched(method, params=None, seconds=20):
            if method == "mcpServerStatus/list":
                return {"data": [{"name": "other", "tools": {}}]}
            return orig(method, params, seconds)
        with self.assertRaisesRegex(RuntimeError, "unavailable"):
            self.probe.run_probe(patched)

    def test_sign_in_required(self):
        legacy, manifest = go_tool_names()
        with self.assertRaisesRegex(RuntimeError, "login stage"):
            self.probe.run_probe(StubCall(legacy | manifest, account="none"))

    def test_profile_required(self):
        legacy, manifest = go_tool_names()
        bad = canned_config(default_permissions="other")
        with self.assertRaisesRegex(RuntimeError, "named native profile"):
            self.probe.run_probe(StubCall(legacy | manifest, config=bad))
        legacy_sandbox = canned_config(sandbox_mode="read-only")
        with self.assertRaisesRegex(RuntimeError, "named native profile"):
            self.probe.run_probe(StubCall(legacy | manifest, config=legacy_sandbox))

    def test_model_effort_gating(self):
        legacy, manifest = go_tool_names()
        stub = StubCall(legacy | manifest)
        self.probe.run_probe(stub, "m", "e")
        with self.assertRaisesRegex(RuntimeError, "not available"):
            self.probe.run_probe(StubCall(legacy | manifest), "m", "nope")

    def test_tool_pages_cursor(self):
        seen = []

        def call(method, params=None, seconds=20):
            seen.append(params.get("cursor"))
            if params.get("cursor") is None:
                return {"data": [], "nextCursor": "c1"}
            return {"data": [{"x": 1}]}
        pages = self.probe.collect_pages(call, "m", {})
        self.assertEqual(len(pages), 2)
        self.assertEqual(seen, [None, "c1"])


class TestPins(unittest.TestCase):
    def test_pins_file_shape(self):
        proc = subprocess.run([sys.executable, str(HERE / "verify-runtime-pins.py"), "pins-file"],
                              stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=60)
        self.assertEqual(proc.returncode, 0, proc.stdout.decode())

    def test_python_floor(self):
        proc = subprocess.run([sys.executable, str(HERE / "verify-runtime-pins.py"),
                               "python", sys.executable],
                              stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=60)
        self.assertEqual(proc.returncode, 0, proc.stdout.decode())

    def test_wrong_binaries_rejected(self):
        for args in (["codex", "/bin/echo"], ["headless-shell", "/bin/echo"],
                     ["python", "/bin/echo"]):
            proc = subprocess.run([sys.executable, str(HERE / "verify-runtime-pins.py"), *args],
                                  stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=60)
            self.assertNotEqual(proc.returncode, 0, args)

    def test_spoofed_version_still_needs_digest(self):
        with tempfile.TemporaryDirectory() as tmp:
            fake = Path(tmp) / "codex"
            fake.write_text('#!/bin/sh\necho "codex-cli 0.153.4"\n')
            fake.chmod(0o755)
            proc = subprocess.run([sys.executable, str(HERE / "verify-runtime-pins.py"),
                                   "codex", str(fake)],
                                  stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=60)
            self.assertNotEqual(proc.returncode, 0, proc.stdout.decode())
            self.assertIn("digest mismatch", proc.stdout.decode())

    def test_env_pin_matches_runtime_pins(self):
        pins = {}
        for line in (HERE / "runtime-pins.txt").read_text().splitlines():
            line = line.split("#", 1)[0].strip()
            if line.startswith("headless_shell "):
                for pair in line.split()[1:]:
                    key, _, value = pair.partition("=")
                    pins[key] = value
        env = (HERE / "api.env.template").read_text()
        match = re.search(r"^JOBSEEK_RESEARCH_CHROME_SHA256=(\w+)$", env, re.M)
        self.assertTrue(match)
        self.assertEqual(match.group(1), pins["linux64_binary_sha256"])
        for var in ("JOBSEEK_ARTIFACT_ROOT", "JOBSEEK_RESEARCH_SCRATCH_ROOT",
                    "JOBSEEK_RESEARCH_CHROME_PATH", "JOBSEEK_RESEARCH_PYTHON_PATH",
                    "JOBSEEK_RESEARCH_SANDBOX_BINARY"):
            self.assertIn(var + "=", env)


PROFILE = '(version 1)(allow default)(deny network-outbound)(allow network-outbound (remote ip "localhost:41234"))'


def sh_source(*exprs):
    script = ". ./executor-sandbox.sh; set +e; " + "; ".join(exprs)
    return subprocess.run(["sh", "-c", script], cwd=HERE,
                          stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=60)


class TestSandboxWrapper(unittest.TestCase):
    def test_parser(self):
        proc = sh_source('parse_proxy_port %s; echo "rc=$?"' % quoted(PROFILE))
        self.assertEqual(proc.stdout.decode(), "41234rc=0\n")
        for bad in ("garbage", "(remote ip \"localhost:99999\"))",
                    "(remote ip \"localhost:0\"))", ""):
            proc = sh_source('parse_proxy_port %s >/dev/null; echo "rc=$?"' % quoted(bad))
            self.assertEqual(proc.stdout.decode(), "rc=1\n", bad)

    def test_preflight_fails_closed(self):
        wrapper = str(HERE / "executor-sandbox.sh")
        cases = [[], ["-x", PROFILE, "/bin/echo"],
                 ["-p", "garbage", "/bin/echo"],
                 ["-p", PROFILE, "/nonexistent-target"]]
        for args in cases:
            proc = subprocess.run([wrapper, *args], cwd=tempfile.gettempdir(),
                                  stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=60)
            self.assertEqual(proc.returncode, 126, (args, proc.stdout.decode()))

    def test_passthrough_when_runnable(self):
        # On a Linux host with the deps this genuinely executes through the
        # namespace; elsewhere the wrapper fails closed. Either way a zero
        # exit must carry the exact child output, never a partial run.
        proc = subprocess.run(
            [str(HERE / "executor-sandbox.sh"), "-p", PROFILE, "/bin/echo", "hi"],
            cwd=tempfile.gettempdir(),
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=120)
        if proc.returncode == 0:
            self.assertEqual(proc.stdout.decode(), "hi\n")


def quoted(value):
    return "'" + value.replace("'", "'\\''") + "'"


class EchoServer:
    def __init__(self, path, work):
        self.proc = subprocess.Popen(
            [sys.executable, str(path)], cwd=work,
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
        line = self.proc.stdout.readline()
        self.port = int(line.strip())

    def close(self):
        try:
            self.proc.kill()
        finally:
            self.proc.stdout.close()
            self.proc.wait(timeout=10)


class TestConfinementProbe(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        work = Path(cls.tmp.name)
        cls.echo_src = work / "echo_server.py"
        cls.nsprobe = work / "nsprobe.py"
        probe = HERE / "accept-executor-confinement.sh"
        bodies = re.findall(r"<<'PY'\n(.*?)\nPY", probe.read_text(), re.S)
        assert len(bodies) == 2, bodies
        cls.echo_src.write_text(bodies[0] + "\n")
        cls.nsprobe.write_text(bodies[1] + "\n")
        import py_compile
        py_compile.compile(str(cls.echo_src), doraise=True)
        py_compile.compile(str(cls.nsprobe), doraise=True)

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def test_echo_round_trip(self):
        server = EchoServer(self.echo_src, self.tmp.name)
        try:
            proc = subprocess.run([sys.executable, str(self.nsprobe), "echo",
                                   str(server.port), "confined-ok"],
                                  stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=30)
            self.assertEqual(proc.returncode, 0, proc.stdout.decode())
        finally:
            server.close()

    def test_refuse_distinguishes_open_and_closed(self):
        server = EchoServer(self.echo_src, self.tmp.name)
        try:
            # Open port: refuse must FAIL (connect succeeds) so the probe's
            # negative control is meaningful.
            proc = subprocess.run([sys.executable, str(self.nsprobe), "refuse", str(server.port)],
                                  stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=30)
            self.assertEqual(proc.returncode, 1)
        finally:
            server.close()
        proc = subprocess.run([sys.executable, str(self.nsprobe), "refuse", str(server.port)],
                              stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=30)
        self.assertEqual(proc.returncode, 0)

    def test_probe_gates_user(self):
        if os.environ.get("USER") == "jobseek-api":
            self.skipTest("running as the service user")
        proc = subprocess.run([str(HERE / "accept-executor-confinement.sh"), "/bin/echo"],
                              stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=60)
        self.assertEqual(proc.returncode, 2)


class TestIdle(unittest.TestCase):
    def test_snapshot_queries(self):
        script = (HERE / "accept-idle.sh").read_text()
        bodies = re.findall(r"<<'PY'\n(.*?)\nPY", script, re.S)
        self.assertEqual(len(bodies), 1)
        with tempfile.TemporaryDirectory() as tmp:
            db_path = str(Path(tmp) / "idle.sqlite")
            db = sqlite3.connect(db_path)
            db.execute("CREATE TABLE rounds (id TEXT)")
            db.execute("CREATE TABLE round_attempts (id TEXT)")
            db.execute("CREATE TABLE jobs (id TEXT)")
            db.execute("CREATE TABLE job_attempts (id TEXT)")
            db.execute("CREATE TABLE run_events (event_id TEXT)")
            db.execute("CREATE TABLE research_requests (state TEXT)")
            db.execute("CREATE TABLE run_checkpoints (active_claims_json TEXT)")
            db.execute("CREATE TABLE delivery_items (state TEXT)")
            db.execute("INSERT INTO rounds VALUES ('r1'), ('r2')")
            db.execute("INSERT INTO research_requests VALUES ('claimed'), ('fresh')")
            db.execute("INSERT INTO run_checkpoints VALUES ('[]'), ('[\"a\"]')")
            db.execute("INSERT INTO delivery_items VALUES ('sending'), ('failed')")
            db.commit()
            db.close()
            proc = subprocess.run([sys.executable, "-c", bodies[0], db_path],
                                  stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=60)
        self.assertEqual(proc.returncode, 0, proc.stdout.decode())
        got = dict(line.split() for line in proc.stdout.decode().splitlines())
        self.assertEqual(got, {"rounds": "2", "round_attempts": "0", "jobs": "0",
                               "job_attempts": "0", "run_events": "0", "claimed": "1",
                               "active_claims": "1", "sending": "1"})
        # The awk gates must read the labels the snapshot prints.
        for label in ("claimed", "active_claims", "sending"):
            self.assertIn('$1=="%s"' % label, script)

    def test_scratch_gate_present(self):
        script = (HERE / "accept-idle.sh").read_text()
        self.assertIn("SCRATCH_DIR", script)
        self.assertIn("executor scratch not empty while idle", script)


class TestShellSyntax(unittest.TestCase):
    def test_all_scripts_parse(self):
        for path in sorted(HERE.glob("*.sh")):
            proc = subprocess.run(["sh", "-n", str(path)],
                                  stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=60)
            self.assertEqual(proc.returncode, 0, (path.name, proc.stdout.decode()))

    def test_installer_gates(self):
        proc = subprocess.run([str(HERE / "install-app.sh")],
                              stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=60)
        self.assertEqual(proc.returncode, 2)
        self.assertIn("run as root", proc.stdout.decode())


if __name__ == "__main__":
    unittest.main(verbosity=2)
