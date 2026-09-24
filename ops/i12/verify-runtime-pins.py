#!/usr/bin/env python3
"""Verify installed execution runtimes against ops/i12/runtime-pins.txt.

Checks version identity plus sha256 digests; any mismatch fails. Roles:
  codex          App Server binary (runner VM, or build-machine reference)
  headless-shell isolated browser backend (app host)
  python         system python3 floor version (app host)

Usage:
  verify-runtime-pins.py codex BINARY [EXPECTED_SHA256]
  verify-runtime-pins.py headless-shell BINARY [PLATFORM]
  verify-runtime-pins.py python [BINARY]
  verify-runtime-pins.py pins-file   # validate runtime-pins.txt shape only

PLATFORM is linux64 (default) or macos-arm64. With no EXPECTED_SHA256 the
codex check uses the macOS reference digest (build-machine re-verification);
on the runner VM always pass the approved Linux digest from the private
deployment record.
"""
import hashlib
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent


def load_pins():
    pins = {}
    for line in (HERE / "runtime-pins.txt").read_text().splitlines():
        line = line.split("#", 1)[0].strip()
        if not line:
            continue
        role, *pairs = line.split()
        entry = {}
        for pair in pairs:
            key, sep, value = pair.partition("=")
            if not sep or not value or key in entry:
                raise SystemExit("malformed pins entry: " + line)
            entry[key] = value
        if role in pins:
            raise SystemExit("duplicate pins role: " + role)
        pins[role] = entry
    for role, keys in {
        "codex": {"version", "macos_sha256"},
        "headless_shell": {"playwright_core", "build", "browser_version", "macos_arm64_sha256",
                           "linux64_zip_sha256", "linux64_binary_sha256", "linux64_url"},
        "python": {"binary", "min_version"},
        "typst": {"version"},
        "toolchain": {"go", "node", "bun"},
    }.items():
        if set(pins.get(role, {})) != keys:
            raise SystemExit("pins role %s has missing or unreviewed keys" % role)
    for key in ("macos_sha256", "macos_arm64_sha256", "linux64_zip_sha256", "linux64_binary_sha256"):
        holder = pins["codex"] if key == "macos_sha256" else pins["headless_shell"]
        digest = holder[key]
        if len(digest) != 64 or any(c not in "0123456789abcdef" for c in digest):
            raise SystemExit("pins digest %s is not 64 lowercase hex chars" % key)
    if pins["headless_shell"]["linux64_url"] != (
            "https://cdn.playwright.dev/builds/cft/%s/linux64/chrome-headless-shell-linux64.zip"
            % pins["headless_shell"]["browser_version"]):
        raise SystemExit("headless-shell URL does not match the pinned browser version")
    return pins


def sha256_file(path):
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def run_version(binary, *args):
    proc = subprocess.run([binary, *args], stdout=subprocess.PIPE,
                          stderr=subprocess.STDOUT, timeout=30)
    if proc.returncode != 0:
        raise SystemExit("version probe failed for " + binary)
    return proc.stdout.decode().strip()


def check_codex(pins, binary, expected=None):
    if run_version(binary, "--version") != " ".join(pins["codex"]["version"].rsplit("-", 1)):
        raise SystemExit("Codex protocol baseline mismatch")
    want = expected or pins["codex"]["macos_sha256"]
    if len(want) != 64 or any(c not in "0123456789abcdef" for c in want):
        raise SystemExit("expected Codex digest is not 64 lowercase hex chars")
    if sha256_file(binary) != want:
        raise SystemExit("Codex binary digest mismatch")
    print("codex %s digest verified." % pins["codex"]["version"])


def check_shell(pins, binary, platform="linux64"):
    key = {"linux64": "linux64_binary_sha256", "macos-arm64": "macos_arm64_sha256"}[platform]
    if pins["headless_shell"]["browser_version"] not in run_version(binary, "--version"):
        raise SystemExit("headless-shell version mismatch")
    if sha256_file(binary) != pins["headless_shell"][key]:
        raise SystemExit("headless-shell binary digest mismatch")
    print("headless-shell %s (%s) digest verified."
          % (pins["headless_shell"]["browser_version"], platform))


def check_python(pins, binary="python3"):
    out = run_version(binary, "--version")
    prefix, _, version = out.partition("Python ")
    try:
        major, minor, *_ = (int(p) for p in version.split("."))
    except ValueError:
        raise SystemExit("cannot parse python version from: " + out)
    want_major, want_minor = (int(p) for p in pins["python"]["min_version"].split("."))
    if (major, minor) < (want_major, want_minor):
        raise SystemExit("python %s below floor %s" % (version, pins["python"]["min_version"]))
    print("python %s meets floor %s." % (version, pins["python"]["min_version"]))


def main():
    pins = load_pins()
    if len(sys.argv) == 2 and sys.argv[1] == "pins-file":
        print("runtime-pins.txt shape ok.")
        return
    if len(sys.argv) >= 3 and sys.argv[1] == "codex" and len(sys.argv) <= 4:
        check_codex(pins, sys.argv[2], sys.argv[3] if len(sys.argv) == 4 else None)
    elif len(sys.argv) >= 3 and sys.argv[1] == "headless-shell" and len(sys.argv) <= 4:
        platform = sys.argv[3] if len(sys.argv) == 4 else "linux64"
        if platform not in ("linux64", "macos-arm64"):
            raise SystemExit("PLATFORM must be linux64 or macos-arm64")
        check_shell(pins, sys.argv[2], platform)
    elif len(sys.argv) >= 2 and sys.argv[1] == "python" and len(sys.argv) <= 3:
        check_python(pins, sys.argv[2] if len(sys.argv) == 3 else "python3")
    else:
        raise SystemExit("usage: verify-runtime-pins.py codex|headless-shell|python|pins-file ...")


if __name__ == "__main__":
    main()
