#!/usr/bin/env python3
"""Fail packaging when the pinned MCP allowlist drifts from runtime readiness."""
import ast
import re
import tomllib
from pathlib import Path

root = Path(__file__).resolve().parents[2]
source = (root / "apps/api/internal/codexservice/tools.go").read_text()
match = re.search(r"var requiredTools = \[\]string\{([^}]+)\}", source)
if not match:
    raise SystemExit("cannot find codexservice requiredTools")
required = set(re.findall(r'"([a-z_]+)"', match.group(1)))
config = tomllib.loads((Path(__file__).parent / "runner-config.toml.template").read_text())
configured = set(config["mcp_servers"]["jobseek"]["enabled_tools"])
tree = ast.parse((Path(__file__).parent / "accept-live-runner.py").read_text())
probe = None
for node in tree.body:
    if isinstance(node, ast.Assign) and any(isinstance(target, ast.Name) and target.id == "REQUIRED_TOOLS" for target in node.targets):
        probe = set(ast.literal_eval(node.value))
        break
if not required or required != configured or required != probe:
    raise SystemExit("required MCP tools differ across runtime, config and acceptance probe")
print("Required MCP tool allowlists match.")
