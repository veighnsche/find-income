#!/bin/sh
set -eu

artifacts=${1:?usage: verify-artifacts.sh ABSOLUTE_ARTIFACT_DIRECTORY CODEX_SHA256 TYPST_SHA256}
codex_sha=${2:?missing pinned Codex SHA-256}
typst_sha=${3:?missing pinned Typst SHA-256}
case "$artifacts" in /*) ;; *) echo 'artifact directory must be absolute' >&2; exit 2;; esac
case "$codex_sha$typst_sha" in *[!0123456789abcdef]*) echo 'pins must be lowercase hex' >&2; exit 2;; esac
[ "${#codex_sha}" -eq 64 ] && [ "${#typst_sha}" -eq 64 ] || { echo 'pins must be 64 hex characters' >&2; exit 2; }
[ -x "$artifacts/bin/jobseek-api" ] && [ -x "$artifacts/bin/codex-runner" ] && [ -x "$artifacts/bin/codex" ] && [ -x "$artifacts/bin/typst" ] && [ -f "$artifacts/web/index.html" ] || { echo 'missing build/runtime artifact' >&2; exit 2; }
(cd "$artifacts" && sha256sum -c --status SHA256SUMS)
printf '%s  %s\n' "$codex_sha" "$artifacts/bin/codex" | sha256sum -c --status -
printf '%s  %s\n' "$typst_sha" "$artifacts/bin/typst" | sha256sum -c --status -
[ "$("$artifacts/bin/codex" --version)" = 'codex-cli 0.153.4' ] || { echo 'Codex protocol baseline mismatch' >&2; exit 2; }
"$artifacts/bin/typst" --version | grep -Eq '^typst 0\.15\.1([[:space:]]|$)' || { echo 'Typst version mismatch' >&2; exit 2; }
echo 'Artifact versions and external hashes verified; host isolation is not established.'
