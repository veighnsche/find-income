#!/bin/sh
set -eu

# Produce code-owned artifacts only. Codex, Typst, keys and service secrets are
# installed separately from approved platform artifacts on the selected hosts.
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
out=${1:?usage: build.sh ABSOLUTE_OUTPUT_DIRECTORY}
case "$out" in /*) ;; *) echo 'output directory must be absolute' >&2; exit 2;; esac
mkdir -p "$out/bin" "$out/web"
python3 "$repo/ops/i12/check-runtime-tools.py"
python3 "$repo/ops/i12/test-probes.py"
(cd "$repo/apps/api" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -o "$out/bin/jobseek-api" ./cmd/server && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -o "$out/bin/codex-runner" ./cmd/codex-runner)
(cd "$repo" && pnpm --filter @jobseek/web build)
cp -R "$repo/apps/web/dist/." "$out/web/"
chmod 0755 "$out/bin/jobseek-api" "$out/bin/codex-runner"
python3 - "$out" <<'PY'
import hashlib, pathlib, sys
root = pathlib.Path(sys.argv[1])
files = [root / "bin/jobseek-api", root / "bin/codex-runner"] + sorted(p for p in (root / "web").rglob("*") if p.is_file())
with (root / "SHA256SUMS").open("w") as manifest:
    for path in files:
        manifest.write(hashlib.sha256(path.read_bytes()).hexdigest() + "  " + path.relative_to(root).as_posix() + "\n")
PY
echo 'Built Linux amd64 API, runner and web assets. No external runtime artifacts or secrets included.'
