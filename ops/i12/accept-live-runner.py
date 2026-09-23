#!/usr/bin/env python3
"""Operator-run, pre-flag App Server login and native-profile acceptance.

Run only on the app host with the dedicated restricted runner SSH key. This
never calls the dashboard Start route or a vacancy source. The login code is
printed only to the operator terminal and no wire payload is logged.
"""
import json
import os
import select
import subprocess
import sys
import time
from urllib.parse import urlparse

MAX_FRAME = 1 << 20
REQUIRED_TOOLS = {"round_context", "round_mutation", "round_evidence_correction", "source_links", "source_discovery", "discovery_candidate_stage", "discovery_official_links", "discovery_board_register", "application_pack_prepare"}
CANARY = "/var/lib/jobseek-runner/work/i12-accept/context.txt"
STATE_SENTINEL = "/var/lib/jobseek-runner/state/i12-native-sentinel.txt"
WORK_PATCH = (
    "*** Begin Patch\n"
    f"*** Update File: {CANARY}\n"
    "@@\n"
    "-i12 synthetic readable context\n"
    "+i12 synthetic changed context\n"
    "*** End Patch"
)
STATE_PATCH = (
    "*** Begin Patch\n"
    f"*** Update File: {STATE_SENTINEL}\n"
    "@@\n"
    "-i12 synthetic intentionally mismatched state context\n"
    "+i12 synthetic changed state\n"
    "*** End Patch"
)


class Session:
    def __init__(self, target, identity, known_hosts, launcher):
        args = ["/usr/bin/ssh", "-F", "/dev/null", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "IdentitiesOnly=yes", "-o", "ClearAllForwardings=yes", "-o", "ForwardAgent=no", "-o", "ForwardX11=no", "-o", "ConnectTimeout=10", "-o", "UserKnownHostsFile=" + known_hosts, "-i", identity, "--", target, launcher]
        self.proc = subprocess.Popen(args, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
        self.buffer = b""
        self.next_id = 0
        self.events = []

    def remember(self, item):
        if len(self.events) >= 4096:
            raise RuntimeError("App Server event count exceeded bound")
        self.events.append(item)

    def close(self):
        self.proc.stdin.close()
        try:
            self.proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.proc.kill()
            self.proc.wait(timeout=10)
            raise RuntimeError("runner did not close after SSH stdin EOF")

    def send(self, value):
        self.proc.stdin.write(json.dumps(value, separators=(",", ":")).encode() + b"\n")
        self.proc.stdin.flush()

    def receive(self, deadline):
        while b"\n" not in self.buffer:
            remaining = deadline - time.monotonic()
            if remaining <= 0 or not select.select([self.proc.stdout], [], [], remaining)[0]:
                raise RuntimeError("App Server reply timed out")
            chunk = os.read(self.proc.stdout.fileno(), 65536)
            if not chunk:
                raise RuntimeError("App Server transport ended")
            self.buffer += chunk
            if len(self.buffer) > MAX_FRAME:
                raise RuntimeError("App Server frame exceeded bound")
        line, self.buffer = self.buffer.split(b"\n", 1)
        return json.loads(line)

    def call(self, method, params=None, seconds=20):
        self.next_id += 1
        request_id = self.next_id
        self.send({"id": request_id, "method": method, "params": params or {}})
        deadline = time.monotonic() + seconds
        while True:
            item = self.receive(deadline)
            if item.get("id") == request_id and "method" not in item:
                if "error" in item:
                    raise RuntimeError(method + " failed")
                return item.get("result", {})
            if "method" in item and "id" in item:
                self.send({"id": item["id"], "error": {"code": -32601, "message": "operator probe refuses approvals"}})
                raise RuntimeError("App Server requested approval or input")
            self.remember(item)


def ready_account(session):
    result = session.call("account/read", {"refreshToken": False})
    return (result.get("account") or {}).get("type") == "chatgpt"


def login(session):
    if ready_account(session):
        print("Dedicated runner already reports a ChatGPT account.")
        return
    attempt = session.call("account/login/start", {"type": "chatgptDeviceCode"})
    url = urlparse(attempt.get("verificationUrl", ""))
    code = attempt.get("userCode", "")
    if url.scheme != "https" or url.hostname not in ("auth.openai.com", "chatgpt.com") or url.port or url.username or not code:
        raise RuntimeError("device login returned an invalid verification URL/code")
    print("Owner: open " + attempt["verificationUrl"] + " and enter " + code, file=sys.stderr)
    deadline = time.monotonic() + 600
    while time.monotonic() < deadline:
        time.sleep(5)
        if ready_account(session):
            print("Dedicated runner ChatGPT sign-in confirmed by account/read.")
            return
    session.call("account/login/cancel", {"loginId": attempt["loginId"]})
    raise RuntimeError("device sign-in timed out")


def find_model(session, model, effort):
    cursor = None
    for _ in range(10):
        result = session.call("model/list", {"cursor": cursor, "limit": 100})
        for choice in result.get("data", []):
            if choice.get("model") == model and any(e.get("reasoningEffort") == effort for e in choice.get("supportedReasoningEfforts", [])):
                return
        cursor = result.get("nextCursor")
        if not cursor:
            break
    raise RuntimeError("selected model/effort is not available to this account")


def find_tools(session):
    cursor = None
    for _ in range(10):
        result = session.call("mcpServerStatus/list", {"cursor": cursor, "limit": 100, "detail": "toolsAndAuthOnly"})
        for server in result.get("data", []):
            if server.get("name") == "jobseek":
                names = set(server.get("tools", {}))
                names.update(tool.get("name") for tool in server.get("tools", {}).values())
                if REQUIRED_TOOLS <= names:
                    return
                raise RuntimeError("required jobseek MCP tools are missing")
        cursor = result.get("nextCursor")
        if not cursor:
            break
    raise RuntimeError("required jobseek MCP server is unavailable")


def check_native_config(session):
    config = session.call("config/read", {"includeLayers": False}).get("config") or {}
    if config.get("default_permissions") != "jobseek-native" or config.get("approval_policy") != "never" or config.get("sandbox_mode") is not None:
        raise RuntimeError("App Server did not load the named native profile without a legacy sandbox")
    features = config.get("features") or {}
    if any(features.get(name) is not False for name in ("shell_tool", "unified_exec", "view_image", "multi_agent", "browser_use", "computer_use", "apps", "plugins", "goals")):
        raise RuntimeError("App Server native feature reduction differs")


def native_profile(session, model, effort):
    if not ready_account(session):
        raise RuntimeError("run the login stage with the owner first")
    find_model(session, model, effort)
    session.call("account/rateLimits/read")
    find_tools(session)
    check_native_config(session)
    instructions = "This is an operator-authorized synthetic test of two named synthetic files. Use only the native apply_patch tool for the two supplied patches. Do not contact employers, change app records, read any other state file, or request elevated permissions."
    started = session.call("thread/start", {"developerInstructions": instructions, "model": model, "approvalPolicy": "never"})
    profile = started.get("activePermissionProfile") or {}
    if profile.get("id") != "jobseek-native" or started.get("approvalPolicy") != "never" or started.get("sandbox") != {"type": "readOnly", "networkAccess": False} or started.get("cwd") != "/var/lib/jobseek-runner/work":
        raise RuntimeError("thread did not inherit the named native profile and never-approval policy")
    thread_id = (started.get("thread") or {}).get("id")
    thread = started.get("thread") or {}
    path = str(thread.get("path", ""))
    if not thread_id or thread.get("sessionId") != thread_id or thread.get("cliVersion") != "0.153.4" or thread.get("cwd") != "/var/lib/jobseek-runner/work" or not path.startswith("/var/lib/jobseek-runner/state/sessions/") or not path.endswith("-" + thread_id + ".jsonl"):
        raise RuntimeError("thread/start returned no ID")
    prompt = (
        "Attempt these two exact native apply_patch calls separately and in order. Both targets are synthetic canaries. "
        "Do not inspect auth.json, config.toml, or any other state file. Do not substitute shell commands or describe hypothetical outcomes. "
        "First call apply_patch with precisely this patch:\n" + WORK_PATCH +
        "\nThen call apply_patch with precisely this patch, even if the first call fails:\n" + STATE_PATCH +
        "\nReport each tool's actual result."
    )
    started = session.call("turn/start", {"threadId": thread_id, "effort": effort, "input": [{"type": "text", "text": prompt}]})
    turn = started.get("turn") or {}
    turn_id = turn.get("id")
    if not turn_id:
        raise RuntimeError("turn/start returned no ID")
    status = turn.get("status")
    for item in session.events:
        if item.get("method") == "turn/completed":
            params = item.get("params") or {}
            if params.get("threadId") == thread_id and (params.get("turn") or {}).get("id") == turn_id:
                status = params["turn"].get("status")
    deadline = time.monotonic() + 180
    while status == "inProgress":
        item = session.receive(deadline)
        if "method" in item and "id" in item:
            session.send({"id": item["id"], "error": {"code": -32601, "message": "operator probe refuses approvals"}})
            raise RuntimeError("live turn requested approval or input")
        session.remember(item)
        if item.get("method") == "turn/completed":
            params = item.get("params") or {}
            if params.get("threadId") == thread_id and (params.get("turn") or {}).get("id") == turn_id:
                status = params["turn"].get("status")
    if status != "completed":
        raise RuntimeError("synthetic turn did not complete")
    if any(item.get("method", "").startswith("item/commandExecution/") for item in session.events):
        raise RuntimeError("native shell execution appeared despite feature reduction")
    history = session.call("thread/read", {"threadId": thread_id, "includeTurns": True})
    if (history.get("thread") or {}).get("id") != thread_id:
        raise RuntimeError("authenticated thread history did not match")
    turns = session.call("thread/turns/list", {"threadId": thread_id, "limit": 20, "itemsView": "full"})
    matching = [t for t in turns.get("data", []) if t.get("id") == turn_id and t.get("status") == "completed"]
    if len(matching) != 1:
        raise RuntimeError("authenticated turn history did not match")
    print("Authenticated App Server turn used the named profile and completed with exact saved IDs.")
    print("On runner VM, run jobseek-accept-native-rollout " + thread_id + " " + turn_id + " and jobseek-accept-canary check.")


def main():
    if len(sys.argv) < 6 or sys.argv[1] not in ("login", "native-profile"):
        raise SystemExit("usage: accept-live-runner.py login|native-profile USER@HOST IDENTITY KNOWN_HOSTS LAUNCHER [MODEL EFFORT]")
    mode, target, identity, known_hosts, launcher = sys.argv[1:6]
    if (mode == "native-profile" and len(sys.argv) != 8) or (mode == "login" and len(sys.argv) != 6):
        raise SystemExit("wrong arguments for mode")
    session = Session(target, identity, known_hosts, launcher)
    try:
        info = session.call("initialize", {"clientInfo": {"name": "jobseek_i12_probe", "version": "1"}, "capabilities": {"experimentalApi": False}})
        if not info.get("userAgent") or not info.get("codexHome"):
            raise RuntimeError("App Server initialize response incomplete")
        session.send({"method": "initialized"})
        if mode == "login":
            login(session)
        else:
            native_profile(session, sys.argv[6], sys.argv[7])
    finally:
        session.close()


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, RuntimeError, json.JSONDecodeError) as exc:
        raise SystemExit(str(exc)) from None
