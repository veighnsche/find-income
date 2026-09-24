#!/usr/bin/env python3
"""Turn-free research readiness probe; run from the app host with the key.

Connects to the pinned App Server through the same restricted SSH launcher,
then checks initialize, ChatGPT sign-in, the named native profile and the
full 17-tool jobseek MCP surface (10 legacy + 7 research) WITHOUT starting
a thread or turn: no spend, no work. Any failure exits nonzero.
"""
import json
import os
import select
import subprocess
import sys
import time

MAX_FRAME = 1 << 20
REQUIRED_TOOLS = {"round_context", "round_mutation", "round_evidence_correction", "source_links", "application_pack_prepare", "offer_comparison_prepare", "interview_prepare", "interview_debrief", "reply_update", "reply_draft", "context_read", "research_memory", "evidence_capture", "jev_assess", "opportunity_match", "records_save", "research_execute"}


def check_server_tools(payload, required=REQUIRED_TOOLS):
    """Assert the jobseek MCP server advertises every required tool."""
    servers = payload.get("data", [])
    for server in servers:
        if server.get("name") != "jobseek":
            continue
        names = set(server.get("tools", {}))
        names.update(tool.get("name") for tool in server.get("tools", {}).values())
        missing = set(required) - names
        if missing:
            raise RuntimeError("required jobseek MCP tools are missing: " + ",".join(sorted(missing)))
        return
    raise RuntimeError("required jobseek MCP server is unavailable")


def check_native_profile(config):
    """Assert the loaded config is the named never-approval profile."""
    if config.get("default_permissions") != "jobseek-native" or \
            config.get("approval_policy") != "never" or config.get("sandbox_mode") is not None:
        raise RuntimeError("App Server did not load the named native profile without a legacy sandbox")
    features = config.get("features") or {}
    if any(features.get(name) is not False for name in ("shell_tool", "unified_exec", "view_image",
                                                        "multi_agent", "browser_use", "computer_use",
                                                        "apps", "plugins", "goals")):
        raise RuntimeError("App Server native feature reduction differs")


def check_model_available(pages, model, effort):
    for page in pages:
        for choice in page.get("data", []):
            if choice.get("model") == model and any(
                    e.get("reasoningEffort") == effort for e in choice.get("supportedReasoningEfforts", [])):
                return
    raise RuntimeError("selected model/effort is not available to this account")


def collect_pages(call, method, params, limit=100, rounds=10):
    pages, cursor = [], None
    for _ in range(rounds):
        page = call(method, dict(params, cursor=cursor, limit=limit))
        pages.append(page)
        cursor = page.get("nextCursor")
        if not cursor:
            break
    return pages


def run_probe(call, model=None, effort=None):
    """Drive the readiness checks over a JSON-RPC call function."""
    info = call("initialize", {"clientInfo": {"name": "jobseek_readiness_probe", "version": "1"}})
    if not info.get("userAgent") or not info.get("codexHome"):
        raise RuntimeError("App Server initialize response incomplete")
    account = call("account/read", {"refreshToken": False})
    if (account.get("account") or {}).get("type") != "chatgpt":
        raise RuntimeError("dedicated runner is not signed in with ChatGPT; run the login stage first")
    config = call("config/read", {"includeLayers": False}).get("config") or {}
    check_native_profile(config)
    for page in collect_pages(call, "mcpServerStatus/list", {"detail": "toolsAndAuthOnly"}):
        try:
            check_server_tools(page)
            break
        except RuntimeError as exc:
            if "unavailable" not in str(exc):
                raise
    else:
        raise RuntimeError("required jobseek MCP server is unavailable")
    call("account/rateLimits/read")
    if model and effort:
        check_model_available(collect_pages(call, "model/list", {}), model, effort)
    return info


class Session:
    def __init__(self, target, identity, known_hosts, launcher):
        args = ["/usr/bin/ssh", "-F", "/dev/null", "-T", "-o", "BatchMode=yes",
                "-o", "StrictHostKeyChecking=yes", "-o", "IdentitiesOnly=yes",
                "-o", "ClearAllForwardings=yes", "-o", "ForwardAgent=no", "-o", "ForwardX11=no",
                "-o", "ConnectTimeout=10", "-o", "UserKnownHostsFile=" + known_hosts,
                "-i", identity, "--", target, launcher]
        self.proc = subprocess.Popen(args, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                     stderr=subprocess.DEVNULL)
        self.buffer = b""
        self.next_id = 0

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
                self.send({"id": item["id"], "error": {"code": -32601, "message": "readiness probe refuses approvals"}})
                raise RuntimeError("App Server requested approval or input")


def main():
    if len(sys.argv) not in (5, 7):
        raise SystemExit("usage: accept-research-readiness.py USER@HOST IDENTITY KNOWN_HOSTS LAUNCHER [MODEL EFFORT]")
    target, identity, known_hosts, launcher = sys.argv[1:5]
    model, effort = (sys.argv[5:7] + [None, None])[:2]
    session = Session(target, identity, known_hosts, launcher)
    try:
        info = run_probe(session.call, model, effort)
    finally:
        session.close()
    print("Research readiness passed: signed-in ChatGPT account, named native profile,")
    print("17 required jobseek MCP tools advertised, no thread or turn started (%s)." % info.get("userAgent"))


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, RuntimeError, json.JSONDecodeError) as exc:
        raise SystemExit(str(exc)) from None
