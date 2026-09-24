#!/usr/bin/env python3
"""Fail packaging when the pinned MCP allowlist drifts from runtime readiness."""
import ast
import re
import runpy
import sys
import tomllib
from pathlib import Path
from urllib.parse import urlparse

root = Path(__file__).resolve().parents[2]
source = (root / "apps/api/internal/codexservice/tools.go").read_text()
match = re.search(r"var requiredTools = \[\]string\{([^}]+)\}", source)
if not match:
    raise SystemExit("cannot find codexservice requiredTools")
required = set(re.findall(r'"([a-z_]+)"', match.group(1)))
template = tomllib.loads((Path(__file__).parent / "runner-config.toml.template").read_text())
configured = set(template["mcp_servers"]["jobseek"]["enabled_tools"])
tree = ast.parse((Path(__file__).parent / "accept-live-runner.py").read_text())
probe = None
for node in tree.body:
    if isinstance(node, ast.Assign) and any(isinstance(target, ast.Name) and target.id == "REQUIRED_TOOLS" for target in node.targets):
        probe = set(ast.literal_eval(node.value))
        break
if not required or required != configured or required != probe:
    raise SystemExit("required MCP tools differ across runtime, config and acceptance probe")
live_patches = runpy.run_path(str(Path(__file__).parent / "accept-live-runner.py"))
rollout_patches = runpy.run_path(str(Path(__file__).parent / "accept-native-rollout.py"))
if any(live_patches[name] != rollout_patches[name] for name in ("WORK_PATCH", "STATE_PATCH")):
    raise SystemExit("live native patches differ from trusted rollout acceptance")

def check_config(config, *, deployed):
    if set(config) != {"forced_login_method", "cli_auth_credentials_store", "web_search", "default_permissions", "approval_policy", "features", "agents", "permissions", "mcp_servers"}:
        raise SystemExit("runner config has missing or unreviewed top-level settings")
    if any(config[key] != value for key, value in {
        "forced_login_method": "chatgpt", "cli_auth_credentials_store": "file", "web_search": "disabled",
        "default_permissions": "jobseek-native", "approval_policy": "never",
    }.items()):
        raise SystemExit("runner account or native approval policy differs")
    if config["features"] != dict.fromkeys(("shell_tool", "unified_exec", "view_image", "multi_agent", "browser_use", "computer_use", "apps", "plugins", "goals"), False) or config["agents"] != {"enabled": False}:
        raise SystemExit("native feature reduction differs")
    profile = config["permissions"]
    if set(profile) != {"jobseek-native"}:
        raise SystemExit("unexpected named permission profile")
    policy = profile["jobseek-native"]
    if set(policy) != {"filesystem", "network"}:
        raise SystemExit("runner permission policy has unreviewed settings")
    if policy["filesystem"] != {
        ":root": "deny", ":minimal": "read",
        "/var/lib/jobseek-runner/state": "deny",
        "/etc/jobseek/runner-config.toml": "deny",
        ":workspace_roots": {".": "read"},
    } or policy["network"] != {"enabled": False}:
        raise SystemExit("runner native filesystem/network boundary differs")
    servers = config["mcp_servers"]
    if set(servers) != {"jobseek"} or set(servers["jobseek"]) != {"url", "required", "default_tools_approval_mode", "enabled_tools", "http_headers"}:
        raise SystemExit("required jobseek MCP settings differ")
    bridge = servers["jobseek"]
    if bridge["default_tools_approval_mode"] != "approve":
        raise SystemExit("jobseek MCP tools must run without native approval prompts")
    url = urlparse(bridge["url"])
    if url.scheme != "https" or not url.hostname or url.username or url.password or url.path != "/api/v1/codex/mcp" or url.query or url.fragment or bridge["required"] is not True or set(bridge["enabled_tools"]) != required or len(bridge["enabled_tools"]) != len(required):
        raise SystemExit("required private jobseek MCP route/tool list differs")
    if set(bridge["http_headers"]) != {"Authorization"} or not bridge["http_headers"]["Authorization"].startswith("Bearer "):
        raise SystemExit("jobseek MCP bearer header differs")
    token = bridge["http_headers"]["Authorization"][7:]
    if deployed and (len(token) < 32 or token.strip() != token or "REPLACE" in token or "PRIVATE_APP_FQDN" in bridge["url"]):
        raise SystemExit("deployed jobseek MCP endpoint or bearer is incomplete")

check_config(template, deployed=False)
if len(sys.argv) > 2:
    raise SystemExit("usage: check-runtime-tools.py [PRIVATE_RUNNER_CONFIG]")
if len(sys.argv) == 2:
    check_config(tomllib.loads(Path(sys.argv[1]).read_text()), deployed=True)
print("Required MCP tools and native permission policy match.")
