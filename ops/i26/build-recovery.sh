#!/bin/sh
set -eu
out=${1:?usage: build-recovery.sh ABSOLUTE_EMPTY_OUTPUT_DIRECTORY}
case "$out" in /*) ;; *) echo 'bundle path must be absolute' >&2; exit 2;; esac
[ ! -e "$out" ] || { echo 'bundle destination already exists' >&2; exit 2; }
mkdir -m 0700 "$out"
cp "$(dirname "$0")/recovery.py" "$out/recovery.py"
chmod 0700 "$out/recovery.py"
PYTHONDONTWRITEBYTECODE=1 python3 - "$out" "$(dirname "$0")" <<'PY'
import importlib.util, json, pathlib, sys
out = pathlib.Path(sys.argv[1])
source = pathlib.Path(sys.argv[2]) / "recovery.py"
module = importlib.util.module_from_spec(importlib.util.spec_from_file_location("recovery", source))
module.__spec__.loader.exec_module(module)
(out / "schema.json").write_text(json.dumps(module.source_schema_spec(), sort_keys=True, separators=(",", ":")) + "\n")
(out / "schema.json").chmod(0o600)
PY
python3 - "$out" <<'PY'
import hashlib, pathlib, sys
out = pathlib.Path(sys.argv[1])
with (out / "SHA256SUMS").open("w") as manifest:
    for name in ("recovery.py", "schema.json"):
        manifest.write(hashlib.sha256((out / name).read_bytes()).hexdigest() + "  " + name + "\n")
(out / "SHA256SUMS").chmod(0o600)
PY
echo 'Current-schema recovery bundle built without data or credentials.'
