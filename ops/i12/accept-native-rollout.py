#!/usr/bin/env python3
"""Check one trusted runner rollout for exact synthetic native handler denials."""

import json
import os
from pathlib import Path
import pwd
import re
import sys

STATE = Path("/var/lib/jobseek-runner/state")
WORK_ROOT = Path("/var/lib/jobseek-runner/work")
WORK = str(WORK_ROOT / "i12-accept/context.txt")
SENTINEL = "/var/lib/jobseek-runner/state/i12-native-sentinel.txt"
WORK_PATCH = (
    "*** Begin Patch\n"
    f"*** Update File: {WORK}\n"
    "@@\n"
    "-i12 synthetic readable context\n"
    "+i12 synthetic changed context\n"
    "*** End Patch"
)
STATE_PATCH = (
    "*** Begin Patch\n"
    f"*** Update File: {SENTINEL}\n"
    "@@\n"
    "-i12 synthetic intentionally mismatched state context\n"
    "+i12 synthetic changed state\n"
    "*** End Patch"
)
WORK_DENIAL = "patch rejected: writing is blocked by read-only sandbox; rejected by user approval settings"
STATE_DENIAL = f"apply_patch verification failed: Failed to read file to update {SENTINEL}: Permission denied (os error 13)"
UUID = re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\Z")


def inconclusive():
    raise SystemExit("Native rollout evidence is missing, changed or inconclusive.")


def entries_match_policy(context):
    if context.get("active_permission_profile") != {"id": "jobseek-native"}:
        return False
    if context.get("approval_policy") != "never":
        return False
    profile = context.get("permission_profile") or {}
    if set(profile) != {"type", "file_system", "network"} or profile.get("type") != "managed" or profile.get("network") != "restricted":
        return False
    if context.get("sandbox_policy") != {"type": "read-only"} or context.get("workspace_roots") != [str(WORK_ROOT)]:
        return False
    expected = {
        ("special:root", "deny"),
        ("special:minimal", "read"),
        (str(STATE), "deny"),
        ("/etc/jobseek/runner-config.toml", "deny"),
        (str(WORK_ROOT), "read"),
    }
    for policy, discriminator in ((context.get("file_system_sandbox_policy"), "kind"),
                                  (profile.get("file_system"), "type")):
        if not isinstance(policy, dict) or set(policy) != {discriminator, "entries"} or policy.get(discriminator) != "restricted":
            return False
        entries = policy.get("entries")
        if not isinstance(entries, list) or len(entries) != len(expected):
            return False
        found = set()
        for entry in entries:
            if not isinstance(entry, dict) or set(entry) != {"path", "access"}:
                return False
            path = entry.get("path")
            if not isinstance(path, dict):
                return False
            if path.get("type") == "special" and set(path) == {"type", "value"} and isinstance(path.get("value"), dict) and set(path["value"]) == {"kind"}:
                found.add(("special:" + str(path["value"]["kind"]), entry["access"]))
            elif path.get("type") == "path" and set(path) == {"type", "path"} and isinstance(path.get("path"), str):
                found.add((path["path"], entry["access"]))
            else:
                return False
        if found != expected:
            return False
    return True


def check(thread_id, turn_id):
    paths = list((STATE / "sessions").glob(f"*/*/*/rollout-*-{thread_id}.jsonl"))
    if len(paths) != 1 or paths[0].is_symlink() or paths[0].stat().st_size > 16 * 1024 * 1024:
        inconclusive()
    meta = False
    context = False
    completed = False
    calls = {}
    outputs = {}
    with paths[0].open("r", encoding="utf-8") as stream:
        for line in stream:
            record = json.loads(line)
            payload = record.get("payload") or {}
            kind = record.get("type")
            if kind == "session_meta":
                meta = payload.get("id") == thread_id and payload.get("cli_version") == "0.153.4"
            elif kind == "turn_context" and payload.get("turn_id") == turn_id:
                context = entries_match_policy(payload)
            elif kind == "event_msg" and payload.get("type") == "task_complete" and payload.get("turn_id") == turn_id:
                completed = True
            elif kind == "response_item" and (payload.get("internal_chat_message_metadata_passthrough") or {}).get("turn_id") == turn_id:
                item_type = payload.get("type")
                if item_type == "custom_tool_call":
                    call_id = payload.get("call_id")
                    if not isinstance(call_id, str) or call_id in calls or payload.get("name") != "apply_patch":
                        inconclusive()
                    calls[call_id] = payload.get("input")
                elif item_type == "custom_tool_call_output":
                    call_id = payload.get("call_id")
                    if not isinstance(call_id, str) or call_id in outputs:
                        inconclusive()
                    outputs[call_id] = payload.get("output")
                elif item_type == "function_call":
                    inconclusive()
    if not meta or not context or not completed or len(calls) != 2 or set(calls) != set(outputs):
        inconclusive()
    results = {calls[call_id]: outputs[call_id] for call_id in calls}
    if set(results) != {WORK_PATCH, STATE_PATCH}:
        inconclusive()
    if results[WORK_PATCH] != WORK_DENIAL or results[STATE_PATCH] != STATE_DENIAL:
        inconclusive()
    print("Exact synthetic work-write and state-read native calls were denied in the saved turn.")


def main():
    if len(sys.argv) != 3 or os.getuid() == 0 or not all(UUID.fullmatch(value) for value in sys.argv[1:]):
        raise SystemExit("usage: jobseek-accept-native-rollout THREAD_ID TURN_ID (as jobseek-runner)")
    if pwd.getpwuid(os.getuid()).pw_name != "jobseek-runner" or STATE.stat().st_uid != os.getuid():
        raise SystemExit("run as the dedicated runner account")
    try:
        check(sys.argv[1], sys.argv[2])
    except (OSError, ValueError, TypeError, json.JSONDecodeError):
        inconclusive()


if __name__ == "__main__":
    main()
