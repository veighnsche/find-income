#!/usr/bin/env python3
"""Current-schema private SQLite, approved-asset and research-artifact recovery; no service control."""
import argparse
import base64
import errno
import hashlib
import json
import os
import pwd
import re
import shutil
import sqlite3
import stat
import subprocess
import sys
import tempfile
from contextlib import closing
from datetime import datetime, timezone
from pathlib import Path

FORMAT = "jobseek-current-backup-v2"
DB_NAME = "jobseek.sqlite"
CAPTURES_DIR = "captures"
EXECUTOR_RECORD = "executor-identity.json"
MAX_BLOB_BYTES = 32 * 1024 * 1024
MAX_RECEIPT_BYTES = 1024 * 1024
SPACE_SLACK_BYTES = 1024 * 1024
BLOB_REF_RE = re.compile(r"\Ablobs/[0-9a-f]{2}/[0-9a-f]{64}\z")
RECEIPT_ID_RE = re.compile(r"\A[A-Za-z0-9_:.~-]{1,128}\z")
HEX_SHA_RE = re.compile(r"\A[0-9a-f]{64}\z")
ASSETS = {
    "cv-vince-liem.typ": "e9643864392f2aff7f900a82714a8feb573f636c24c62e7a169c29de41bc9a57",
    "cv-vince-liem.md": "eaf82b8442ac51007b83397279d7f16e0f4a8547bd63f340253ad8263a9ddf61",
    "github-evidence-review.md": "4bf7467279e2440d7a1e870274726055bd1d33550e7272a7731aa568e0f6c4f3",
    "portfolio-case-studies.md": "67f5355ae303479a363b0040fe9b18732fce308612791f0666bbf7250a33d1c0",
}
SENSITIVE_COLUMNS = {
    ("administrator", "password_hash"), ("auth_sessions", "token_hash"), ("auth_sessions", "csrf_hash"),
    ("agent_credentials", "token_hash"), ("jobs", "lease_token"), ("job_attempts", "lease_token"),
    ("round_tool_capabilities", "token_sha256"),
}
REVIEWED_NONSECRETS = {("administrator", "credential_version"), ("jev_attempts", "input_tokens"), ("jev_attempts", "output_tokens")}
ROOT = Path(__file__).resolve().parents[2]
MIGRATIONS = ROOT / "apps/api/internal/store/migrations"
MIGRATION_TABLE_SQL = """CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY, name TEXT NOT NULL, sha256 TEXT NOT NULL, applied_at TEXT NOT NULL)"""


def fail(message):
    raise ValueError(message)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def file_digest(path):
    h = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def checked_path(raw, *, exists=True, directory=False, group_read=False):
    path = Path(raw)
    if not path.is_absolute() or path == Path("/") or path.is_symlink():
        fail("path must be absolute, non-root and not a symlink")
    if exists:
        if not path.exists() or path.is_dir() != directory:
            fail("expected private path is missing or has wrong type")
        mode = path.stat().st_mode
        if directory and mode & (0o027 if group_read else 0o077):
            fail("private directory must not be group/world accessible")
        if not directory and (not stat.S_ISREG(mode) or mode & (0o027 if group_read else 0o077)):
            fail("private file must be regular and owner-only")
    elif path.exists() or path.is_symlink():
        fail("destination already exists")
    return path


def source_migrations():
    files = sorted(MIGRATIONS.glob("[0-9][0-9][0-9]_*.sql"))
    if not files:
        fail("current schema migration files are unavailable")
    return [(i, file.name, digest(file.read_bytes()), file.read_text()) for i, file in enumerate(files, 1)]


def schema_signature(db):
    rows = db.execute("SELECT type,name,sql FROM sqlite_master WHERE name NOT GLOB 'sqlite_*' ORDER BY type,name").fetchall()
    return digest(json.dumps(rows, separators=(",", ":"), ensure_ascii=False).encode())


def source_schema_spec():
    migrations = source_migrations()
    db = sqlite3.connect(":memory:")
    db.execute(MIGRATION_TABLE_SQL)
    for _, _, _, sql in migrations:
        db.executescript(sql)
    signature = schema_signature(db)
    db.close()
    return {"migrations": [[n, name, sha] for n, name, sha, _ in migrations], "schemaSha256": signature}


def schema_spec():
    packaged = Path(__file__).with_name("schema.json")
    if packaged.exists():
        spec = json.loads(packaged.read_bytes())
        if set(spec) != {"migrations", "schemaSha256"}:
            fail("packaged schema specification is invalid")
        return spec
    return source_schema_spec()


def check_schema(db):
    spec = schema_spec()
    actual = db.execute("SELECT version,name,sha256 FROM schema_migrations ORDER BY version").fetchall()
    if actual != [tuple(row) for row in spec["migrations"]]:
        fail("database migrations differ from the current application schema")
    signature = schema_signature(db)
    if signature != spec["schemaSha256"]:
        fail("database schema objects differ from current application schema")
    found = set()
    for (table,) in db.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT GLOB 'sqlite_*'"):
        for column in db.execute("PRAGMA table_info('" + table.replace("'", "''") + "')"):
            name = column[1]
            if any(word in name.lower() for word in ("token", "secret", "password", "credential", "csrf", "cookie", "authorization")):
                found.add((table, name))
    if found != SENSITIVE_COLUMNS | REVIEWED_NONSECRETS:
        fail("credential-bearing schema columns changed; review sanitization before backup")
    return signature


def check_db(db):
    if db.execute("PRAGMA integrity_check").fetchone()[0] != "ok":
        fail("SQLite integrity check failed")
    if db.execute("PRAGMA foreign_key_check").fetchone() is not None:
        fail("SQLite foreign key check failed")
    return check_schema(db)


def private_file(path):
    if path.is_symlink() or not path.is_file() or path.stat().st_mode & 0o077:
        fail("private artifact file has unsafe permissions or type")


def secret_values_from_files(api_env, ssh_key, tls_key):
    values = []
    api_env = checked_path(api_env)
    for line in api_env.read_text().splitlines():
        name, separator, value = line.partition("=")
        if separator and name.strip() in ("TYPESAFE_API_KEY", "JOBSEEK_CODEX_BRIDGE_TOKEN"):
            value = value.strip().strip('"').strip("'")
            if not value or value.startswith("REPLACE_"):
                fail("required app secret is absent")
            values.append(value.encode())
    if len(values) != 2:
        fail("both app-held provider and bridge secrets are required for scan")
    for raw, group_read in ((ssh_key, False), (tls_key, True)):
        path = checked_path(raw, group_read=group_read)
        body = path.read_bytes()
        values.append(body)
        values.extend(line.strip() for line in body.splitlines() if len(line.strip()) >= 32 and not line.startswith(b"-----"))
    return values


def reject_secret_bytes(paths, values):
    for path in paths:
        body = path.read_bytes()
        if any(value and value in body for value in values):
            fail("known deployment secret appeared in backup output")


def asset_bytes(root, expected):
    result = {}
    for name, sha in expected.items():
        path = root / name
        if path.is_symlink() or not path.is_file() or path.stat().st_size > 100000 or path.stat().st_mode & 0o027:
            fail("approved asset missing, linked or too large")
        body = path.read_bytes()
        if digest(body) != sha:
            fail("approved asset digest differs from pinned source")
        result[name] = body
    if {p.name for p in root.iterdir()} != set(expected):
        fail("asset directory contains unexpected files")
    return result


def pack_hash(manifest, source, pdf):
    body = json.dumps({"Manifest": base64.b64encode(manifest).decode(), "Source": base64.b64encode(source).decode(), "PDF": base64.b64encode(pdf).decode()}, separators=(",", ":")).encode()
    return digest(body)


def check_packs(db, assets):
    count = 0
    for row in db.execute("SELECT opportunity_id,opportunity_revision,profile_revision,content_sha256,manifest_json,typst_source,pdf FROM application_packs"):
        opportunity, revision, profile, sha, manifest, source, pdf = row
        manifest = manifest.encode() if isinstance(manifest, str) else manifest
        source = source.encode() if isinstance(source, str) else source
        pdf = pdf.encode() if isinstance(pdf, str) else pdf
        if not pdf.startswith(b"%PDF-") or pack_hash(manifest, source, pdf) != sha:
            fail("application pack content digest or PDF is invalid")
        item = json.loads(manifest)
        role = item.get("role") or {}
        if (role.get("opportunityId"), role.get("opportunityRevision"), role.get("profileRevision")) != (opportunity, revision, profile):
            fail("application pack manifest references a different role revision")
        sources = item.get("sources") or []
        if not sources:
            fail("application pack has no immutable career sources")
        for source_item in sources:
            name = source_item.get("id")
            if name not in assets or source_item.get("sha256") != digest(assets[name]) or source_item.get("body", "").encode() != assets[name] or source_item.get("approved") is not True:
                fail("application pack career source differs from backed-up approved asset")
        count += 1
    return count


def check_delivery_material(db):
    for mime, mime_sha, attachment_sha, pack_sha, pdf, saved_pack_sha in db.execute(
        "SELECT d.mime_bytes,d.mime_sha256,d.attachment_sha256,d.pack_content_sha256,p.pdf,p.content_sha256 "
        "FROM delivery_items d JOIN application_packs p ON p.id=d.pack_id"
    ):
        if digest(mime) != mime_sha or digest(pdf) != attachment_sha or pack_sha != saved_pack_sha:
            fail("saved delivery MIME or referenced pack differs from its digest")


def require_space(path, needed, what):
    try:
        free = shutil.disk_usage(path).free
    except OSError as exc:
        if exc.errno == errno.ENOSPC:
            fail("storage exhausted: cannot measure free space for " + what)
        raise
    if free < needed:
        fail("storage exhausted: %s needs %d bytes but only %d are free" % (what, needed, free))


def ensure_private_dir(path):
    stack = []
    probe = path
    while not probe.exists():
        stack.append(probe)
        probe = probe.parent
    path.mkdir(parents=True, mode=0o700, exist_ok=True)
    for directory in stack:
        os.chmod(directory, 0o700)
    os.chmod(path, 0o700)


def check_captures(db, root):
    """Verify every source_captures row against its blob bytes and every
    observation receipt_ref against its receipt file, following the
    server-side ResolveReceipt rules (file plus recorded row, fingerprint
    and capture binding). Returns (captures, receipts, executors) where
    captures maps capture_id to {sha256, ref, size}."""
    captures = {}
    for cid, sha, ref, size, executor_raw in db.execute(
            "SELECT id,content_sha256,artifact_ref,byte_length,executor_identity_json FROM source_captures ORDER BY id"):
        if not HEX_SHA_RE.match(sha or ""):
            fail("capture content sha256 has an unsupported shape")
        if not BLOB_REF_RE.match(ref or "") or ref != "blobs/" + sha[:2] + "/" + sha:
            fail("capture artifact ref disagrees with its content sha256")
        path = root / ref
        private_file(path)
        if path.stat().st_size > MAX_BLOB_BYTES:
            fail("capture blob exceeds the backup size bound")
        body = path.read_bytes()
        if len(body) != size or digest(body) != sha:
            fail("capture bytes disagree with the recorded row")
        executor = parse_executor(executor_raw)
        if executor is None:
            fail("capture executor identity names no backend")
        captures[cid] = {"sha256": sha, "ref": ref, "size": size}
    requests = {rid: fp for rid, fp in db.execute("SELECT id,fingerprint FROM research_requests")}
    contents = {cid: sha for cid, sha in db.execute("SELECT id,content_sha256 FROM source_captures")}
    by_content = set(contents.values())
    receipts = []
    for obs_id, request_id, capture_id, receipt_ref in db.execute(
            "SELECT id,request_id,capture_id,receipt_ref FROM research_observations ORDER BY id"):
        if receipt_ref is None:
            continue
        if not RECEIPT_ID_RE.match(receipt_ref):
            fail("observation receipt ref has an unsupported shape")
        path = root / "receipts" / (receipt_ref + ".json")
        private_file(path)
        if path.stat().st_size > MAX_RECEIPT_BYTES:
            fail("receipt file exceeds the backup size bound")
        try:
            rec = json.loads(path.read_bytes())
        except ValueError:
            fail("stored receipt body is not valid JSON")
        if not isinstance(rec, dict) or rec.get("id") != receipt_ref:
            fail("stored receipt disagrees with its recorded ref")
        if rec.get("fingerprint") and rec["fingerprint"] != requests.get(request_id):
            fail("receipt fingerprint disagrees with the recorded request")
        capture_ref = rec.get("captureId") or ""
        if capture_ref and capture_id:
            if contents.get(capture_id) != capture_ref:
                fail("receipt captureId disagrees with the recorded capture")
        elif capture_ref and capture_ref not in by_content:
            fail("receipt captureId names no recorded capture")
        receipts.append(receipt_ref)
    executors = set()
    for (raw,) in db.execute("SELECT executor_identity_json FROM source_captures"):
        executors.add(canonical_executor(raw))
    for (raw,) in db.execute("SELECT executor_identity_json FROM research_observations WHERE executor_identity_json IS NOT NULL"):
        executors.add(canonical_executor(raw))
    return captures, sorted(receipts), [json.loads(raw) for raw in sorted(executors)]


def parse_executor(raw):
    try:
        executor = json.loads(raw)
    except ValueError:
        return None
    if not isinstance(executor, dict) or not executor.get("backend"):
        return None
    return executor


def canonical_executor(raw):
    executor = parse_executor(raw)
    if executor is None:
        fail("recorded executor identity names no backend")
    return json.dumps(executor, sort_keys=True, separators=(",", ":"))


def check_executor_record(record_bytes, executors):
    try:
        record = json.loads(record_bytes)
    except ValueError:
        fail("executor identity record is not valid JSON")
    if not isinstance(record, dict) or set(record) != {"executors"} or not isinstance(record["executors"], list):
        fail("executor identity record has an unsupported shape")
    if sorted(canonical_executor(json.dumps(e)) for e in record["executors"]) != sorted(
            canonical_executor(json.dumps(e)) for e in executors):
        fail("executor identity record differs from the database snapshot")


def unreferenced_artifact_counts(root, refs, receipts):
    """Count artifact-root files the snapshot does not reference. Orphan
    blobs/receipts are skipped by the backup (manifest reconciliation) and
    anything outside blobs/ and receipts/ (restored records, operator
    debris, transient cache) is never copied."""
    orphan_blobs = 0
    blobs = root / "blobs"
    if blobs.is_dir():
        for path in blobs.rglob("*"):
            if path.is_file() and not path.is_symlink() and path.relative_to(root).as_posix() not in refs:
                orphan_blobs += 1
    wanted = {name + ".json" for name in receipts}
    orphan_receipts = 0
    receipts_dir = root / "receipts"
    if receipts_dir.is_dir():
        for path in receipts_dir.iterdir():
            if path.is_file() and not path.is_symlink() and path.name not in wanted:
                orphan_receipts += 1
    ignored = [p.name for p in root.iterdir() if p.name not in ("blobs", "receipts")]
    return orphan_blobs, orphan_receipts, len(ignored)


def check_run_history(db):
    events = db.execute("SELECT COUNT(*) FROM run_events").fetchone()[0]
    checkpoints = db.execute("SELECT COUNT(*) FROM run_checkpoints").fetchone()[0]
    for row in db.execute("SELECT active_claims_json,evidence_ids_json,saved_record_ids_json,unresolved_attempts_json,next_work_json FROM run_checkpoints"):
        for raw in row:
            try:
                json.loads(raw)
            except ValueError:
                fail("checkpoint row carries invalid JSON")
    return events, checkpoints


def sanitize(db):
    now = datetime.now(timezone.utc).isoformat()
    db.execute("PRAGMA foreign_keys=ON")
    db.execute("PRAGMA secure_delete=ON")
    with db:
        db.execute("DELETE FROM auth_sessions")
        db.execute("DELETE FROM agent_credentials")
        db.execute("DELETE FROM administrator")
        db.execute("DELETE FROM round_tool_capabilities")
        db.execute("UPDATE job_attempts SET lease_token='restored-' || lower(hex(randomblob(16)))")
        db.execute("UPDATE job_attempts SET outcome='failed',finished_at=?,error_code='restored_inactive',error_message=NULL WHERE outcome='running'", (now,))
        db.execute("UPDATE jobs SET state='failed',lease_token=NULL,lease_owner=NULL,lease_until=NULL,result_ref=NULL,result_json=NULL,last_error_code='restored_inactive',last_error_message=NULL,updated_at=? WHERE state IN ('queued','running')", (now,))
        db.execute("UPDATE round_attempts SET state='cancelled',error_code='restored_inactive',finished_at=? WHERE state='reserved'", (now,))
        db.execute("UPDATE round_attempts SET state='uncertain',error_code='restored_inactive',finished_at=? WHERE state='dispatched'", (now,))
        db.execute("UPDATE rounds SET state='failed',generation=generation+1,revision=revision+1,stop_reason='restored_inactive',reconciliation_required=1,updated_at=?,completed_at=? WHERE state IN ('queued','running','awaiting_input','stopping','paused')", (now, now))
        db.execute("UPDATE round_reconciliation_checks SET state='unknown',finished_at=? WHERE state='pending'", (now,))
        db.execute("UPDATE jev_attempts SET status='uncertain',finished_at=? WHERE status='dispatched'", (now,))
        db.execute("UPDATE delivery_items SET state='uncertain',smtp_stage='interrupted',smtp_code=0,outcome_detail='send intent existed at restore; submission outcome unknown',updated_at=? WHERE state='sending'", (now,))
        db.execute("UPDATE ingestion_requests SET status='failed',safe_error_code='restored_inactive',updated_at=? WHERE status IN ('pending','processing')", (now,))
        db.execute("DELETE FROM qualification_refresh_queue")
        db.execute("UPDATE research_requests SET state='uncertain',lease_owner=NULL,lease_generation=NULL,lease_until=NULL,updated_at=? WHERE state='claimed'", (now,))
        db.execute("UPDATE run_checkpoints SET active_claims_json='[]',updated_at=?", (now,))
    check_db(db)
    if db.execute("SELECT 1 FROM administrator UNION SELECT 1 FROM auth_sessions UNION SELECT 1 FROM agent_credentials UNION SELECT 1 FROM round_tool_capabilities").fetchone():
        fail("credential rows survived sanitization")
    if db.execute("SELECT 1 FROM research_requests WHERE state='claimed'").fetchone():
        fail("restored database kept a live research claim")
    if db.execute("SELECT 1 FROM run_checkpoints WHERE active_claims_json<>'[]'").fetchone():
        fail("restored database kept active checkpoint claims")
    if db.execute("SELECT 1 FROM jobs WHERE state IN ('queued','running') UNION SELECT 1 FROM rounds WHERE state IN ('queued','running','awaiting_input','stopping','paused') UNION SELECT 1 FROM delivery_items WHERE state='sending'").fetchone():
        fail("restored database would resume recruitment")


def open_ro(path):
    return sqlite3.connect(path.as_uri() + "?mode=ro", uri=True)


def write_new_private(path, body):
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0)
    fd = os.open(path, flags, 0o600)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(body)
    except BaseException:
        path.unlink(missing_ok=True)
        raise


def manifest_capture_files(manifest):
    captures = manifest.get("captures")
    receipts = manifest.get("receipts")
    if not isinstance(captures, dict) or not isinstance(receipts, list):
        fail("backup manifest is not current format")
    blob_files = set()
    for cid, entry in captures.items():
        if not isinstance(entry, dict) or not HEX_SHA_RE.match(entry.get("sha256") or ""):
            fail("backup manifest is not current format")
        if not BLOB_REF_RE.match(entry.get("ref") or "") or not isinstance(entry.get("size"), int):
            fail("backup manifest is not current format")
        blob_files.add(CAPTURES_DIR + "/" + entry["ref"])
    receipt_files = set()
    for name in receipts:
        if not isinstance(name, str) or not RECEIPT_ID_RE.match(name):
            fail("backup manifest is not current format")
        receipt_files.add(CAPTURES_DIR + "/receipts/" + name + ".json")
    return blob_files, receipt_files


def verify_archive(archive, expected_manifest_sha, approved=ASSETS):
    checked_path(archive, directory=True)
    allowed = {"manifest.json", DB_NAME, "assets", CAPTURES_DIR, EXECUTOR_RECORD}
    if {p.name for p in archive.iterdir()} != allowed:
        fail("archive has missing or extra entries")
    manifest_path = archive / "manifest.json"
    db_path = archive / DB_NAME
    executor_path = archive / EXECUTOR_RECORD
    private_file(manifest_path)
    private_file(db_path)
    private_file(executor_path)
    if file_digest(manifest_path) != expected_manifest_sha:
        fail("backup manifest hash differs from the separately recorded pin")
    manifest = json.loads(manifest_path.read_bytes())
    required = {"format", "files", "schemaSha256", "packCount", "captureCount",
                "receiptCount", "runEventCount", "runCheckpointCount", "captures", "receipts"}
    if manifest.get("format") != FORMAT or any(key not in manifest for key in required):
        fail("backup manifest is not current format")
    blob_files, receipt_files = manifest_capture_files(manifest)
    if set(manifest["files"]) != {DB_NAME, EXECUTOR_RECORD} | {"assets/" + name for name in approved} | blob_files | receipt_files:
        fail("backup file list differs from current approved assets")
    for name, sha in manifest["files"].items():
        path = archive / name
        private_file(path)
        if file_digest(path) != sha:
            fail("backup file hash mismatch")
    assets_dir = checked_path(archive / "assets", directory=True)
    assets = asset_bytes(assets_dir, approved)
    captures_root = checked_path(archive / CAPTURES_DIR, directory=True)
    actual_capture_files = {p.relative_to(archive).as_posix() for p in captures_root.rglob("*") if not p.is_dir() or p.is_symlink()}
    if actual_capture_files != blob_files | receipt_files:
        fail("archive captures differ from the manifest file list")
    with closing(open_ro(db_path)) as db:
        schema = check_db(db)
        packs = check_packs(db, assets)
        check_delivery_material(db)
        live_captures, live_receipts, live_executors = check_captures(db, captures_root)
        events, checkpoints = check_run_history(db)
        if schema != manifest["schemaSha256"] or packs != manifest.get("packCount"):
            fail("backup schema or pack count differs from manifest")
        if live_captures != manifest["captures"] or live_receipts != sorted(manifest["receipts"]):
            fail("backup capture manifest differs from the database snapshot")
        if len(live_captures) != manifest.get("captureCount") or len(live_receipts) != manifest.get("receiptCount"):
            fail("backup capture counts differ from manifest")
        if events != manifest.get("runEventCount") or checkpoints != manifest.get("runCheckpointCount"):
            fail("backup event journal or checkpoints differ from manifest")
        check_executor_record(executor_path.read_bytes(), live_executors)
        if db.execute("SELECT 1 FROM administrator UNION SELECT 1 FROM auth_sessions UNION SELECT 1 FROM agent_credentials UNION SELECT 1 FROM round_tool_capabilities").fetchone():
            fail("backup contains credential rows")
        if db.execute("SELECT 1 FROM jobs WHERE state IN ('queued','running') UNION SELECT 1 FROM rounds WHERE state IN ('queued','running','awaiting_input','stopping','paused') UNION SELECT 1 FROM delivery_items WHERE state='sending'").fetchone():
            fail("backup contains runnable recruitment state")
        if db.execute("SELECT 1 FROM research_requests WHERE state='claimed' UNION SELECT 1 FROM run_checkpoints WHERE active_claims_json<>'[]'").fetchone():
            fail("backup contains live research claims")
    return manifest


def create(data_dir, assets_root, artifact_root, output, approved=ASSETS, known_secrets=()):
    data_dir = checked_path(data_dir, directory=True)
    assets_root = checked_path(assets_root, directory=True, group_read=True)
    artifacts = checked_path(artifact_root, directory=True)
    output = checked_path(output, exists=False)
    checked_path(output.parent, directory=True)
    if ROOT in output.parents or data_dir in output.parents or assets_root in output.parents or artifacts in output.parents:
        fail("backup destination must be outside source, data, asset and artifact directories")
    source = data_dir / DB_NAME
    private_file(source)
    assets = asset_bytes(assets_root, approved)
    estimate = source.stat().st_size + sum(len(body) for body in assets.values()) + SPACE_SLACK_BYTES
    for path in artifacts.rglob("*"):
        if path.is_file() and not path.is_symlink():
            estimate += path.stat().st_size
    require_space(output.parent, estimate, "backup archive")
    output.mkdir(mode=0o700)
    try:
        with tempfile.TemporaryDirectory(prefix=".jobseek-snapshot-", dir=output.parent) as scratch:
            os.chmod(scratch, 0o700)
            raw = Path(scratch) / "raw.sqlite"
            with closing(open_ro(source)) as live, closing(sqlite3.connect(raw)) as snapshot:
                live.backup(snapshot)
            os.chmod(raw, 0o600)
            with closing(sqlite3.connect(raw)) as db:
                schema = check_db(db)
                check_packs(db, assets)
                check_delivery_material(db)
                live_captures, live_receipts, live_executors = check_captures(db, artifacts)
                sanitize(db)
                final = output / DB_NAME
                db.execute("VACUUM INTO ?", (str(final),))
            os.chmod(final, 0o600)
        asset_out = output / "assets"
        ensure_private_dir(asset_out)
        for name, body in assets.items():
            path = asset_out / name
            path.write_bytes(body)
            os.chmod(path, 0o600)
        captures_out = output / CAPTURES_DIR
        ensure_private_dir(captures_out / "blobs")
        ensure_private_dir(captures_out / "receipts")
        refs = {entry["ref"] for entry in live_captures.values()}
        for ref in sorted(refs):
            body = (artifacts / ref).read_bytes()
            if digest(body) != ref.rsplit("/", 1)[1]:
                fail("artifact bytes changed during backup")
            dest = captures_out / ref
            ensure_private_dir(dest.parent)
            write_new_private(dest, body)
        for name in sorted(set(live_receipts)):
            write_new_private(captures_out / "receipts" / (name + ".json"), (artifacts / "receipts" / (name + ".json")).read_bytes())
        executor_body = json.dumps({"executors": live_executors}, sort_keys=True, separators=(",", ":")).encode() + b"\n"
        write_new_private(output / EXECUTOR_RECORD, executor_body)
        with closing(open_ro(final)) as db:
            check_db(db)
            count = check_packs(db, assets)
            check_delivery_material(db)
            final_captures, final_receipts, final_executors = check_captures(db, captures_out)
            if final_captures != live_captures or final_receipts != live_receipts or final_executors != live_executors:
                fail("sanitization altered capture evidence")
            events, checkpoints = check_run_history(db)
        orphan_blobs, orphan_receipts, ignored = unreferenced_artifact_counts(artifacts, refs, live_receipts)
        files = {DB_NAME: file_digest(final), EXECUTOR_RECORD: digest(executor_body)}
        files |= {"assets/" + name: digest(body) for name, body in assets.items()}
        for ref in refs:
            files[CAPTURES_DIR + "/" + ref] = file_digest(captures_out / ref)
        for name in sorted(set(live_receipts)):
            rel = CAPTURES_DIR + "/receipts/" + name + ".json"
            files[rel] = file_digest(captures_out / "receipts" / (name + ".json"))
        manifest = {"format": FORMAT, "createdAt": datetime.now(timezone.utc).isoformat(), "schemaSha256": schema,
                    "packCount": count, "captureCount": len(live_captures), "receiptCount": len(live_receipts),
                    "runEventCount": events, "runCheckpointCount": checkpoints,
                    "captures": live_captures, "receipts": live_receipts,
                    "skippedOrphanBlobs": orphan_blobs, "skippedOrphanReceipts": orphan_receipts,
                    "ignoredArtifactEntries": ignored, "files": files}
        manifest_path = output / "manifest.json"
        manifest_path.write_bytes(json.dumps(manifest, sort_keys=True, separators=(",", ":")).encode() + b"\n")
        os.chmod(manifest_path, 0o600)
        reject_secret_bytes([final, manifest_path, output / EXECUTOR_RECORD, *(asset_out / name for name in assets),
                             *(captures_out / ref for ref in refs),
                             *(captures_out / "receipts" / (name + ".json") for name in live_receipts)], known_secrets)
        manifest_sha = file_digest(manifest_path)
        verify_archive(output, manifest_sha, approved)
        return manifest_sha
    except BaseException:
        shutil.rmtree(output, ignore_errors=True)
        raise


def restore(archive, manifest_sha, data_dir, assets_root, artifact_root, approved=ASSETS):
    archive = checked_path(archive, directory=True)
    manifest = verify_archive(archive, manifest_sha, approved)
    data_dir = checked_path(data_dir, directory=True)
    assets_root = checked_path(assets_root, directory=True, group_read=True)
    artifacts = checked_path(artifact_root, directory=True)
    if (data_dir / DB_NAME).exists() or (data_dir / (DB_NAME + "-wal")).exists() or (data_dir / (DB_NAME + "-shm")).exists():
        fail("restore target already contains SQLite state")
    if any(data_dir.iterdir()):
        fail("restore target data directory must be empty")
    if any(artifacts.iterdir()):
        fail("restore target artifact directory must be empty")
    current = {p.name for p in assets_root.iterdir()}
    if current and current != set(approved):
        fail("restore target assets are neither empty nor the pinned set")
    if current:
        asset_bytes(assets_root, approved)
    require_space(data_dir, (archive / DB_NAME).stat().st_size + SPACE_SLACK_BYTES, "restored database")
    captures_size = sum(p.stat().st_size for p in (archive / CAPTURES_DIR).rglob("*") if p.is_file() and not p.is_symlink())
    require_space(artifacts, captures_size + (archive / EXECUTOR_RECORD).stat().st_size + SPACE_SLACK_BYTES, "restored artifacts")
    if shutil.which("systemctl"):
        status = subprocess.run(["systemctl", "show", "--value", "-p", "ActiveState", "jobseek-api.service"], capture_output=True, text=True, check=False)
        if status.returncode != 0 or status.stdout.strip() not in ("inactive", "failed"):
            fail("jobseek-api service must be installed and stopped before restoring")
    user = pwd.getpwnam("jobseek-api") if os.geteuid() == 0 else None
    made = []
    try:
        if not current:
            for name in approved:
                path = assets_root / name
                write_new_private(path, (archive / "assets" / name).read_bytes())
                os.chmod(path, 0o640 if user else 0o600)
                if user:
                    os.chown(path, 0, user.pw_gid)
                made.append(path)
        dest = data_dir / DB_NAME
        fd = os.open(dest, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o600)
        made.append(dest)
        with (archive / DB_NAME).open("rb") as source, os.fdopen(fd, "wb") as target:
            shutil.copyfileobj(source, target, 1 << 20)
        os.chmod(dest, 0o600)
        if user:
            os.chown(dest, user.pw_uid, user.pw_gid)
            os.chown(data_dir, user.pw_uid, user.pw_gid)
            os.chown(assets_root, 0, user.pw_gid)
            os.chmod(assets_root, 0o750)
        if file_digest(dest) != file_digest(archive / DB_NAME):
            fail("restored SQLite file differs from verified backup")
        ensure_private_dir(artifacts / "blobs")
        ensure_private_dir(artifacts / "receipts")
        if user:
            for sub in ("blobs", "receipts"):
                os.chown(artifacts / sub, user.pw_uid, user.pw_gid)
        for rel, sha in sorted(manifest["files"].items()):
            if not rel.startswith(CAPTURES_DIR + "/"):
                continue
            body = (archive / rel).read_bytes()
            if digest(body) != sha:
                fail("backup capture differs from manifest during restore")
            target = artifacts / rel[len(CAPTURES_DIR) + 1:]
            ensure_private_dir(target.parent)
            write_new_private(target, body)
            os.chmod(target, 0o600)
            if user:
                os.chown(target, user.pw_uid, user.pw_gid)
            made.append(target)
        record_body = (archive / EXECUTOR_RECORD).read_bytes()
        if digest(record_body) != manifest["files"][EXECUTOR_RECORD]:
            fail("backup executor record differs from manifest during restore")
        record_target = artifacts / EXECUTOR_RECORD
        write_new_private(record_target, record_body)
        os.chmod(record_target, 0o600)
        if user:
            os.chown(record_target, user.pw_uid, user.pw_gid)
            os.chown(artifacts, user.pw_uid, user.pw_gid)
        made.append(record_target)
        with closing(open_ro(dest)) as db:
            check_db(db)
            restored_captures, restored_receipts, restored_executors = check_captures(db, artifacts)
            if restored_captures != manifest["captures"] or restored_receipts != sorted(manifest["receipts"]):
                fail("restored artifacts differ from the backup manifest")
            check_executor_record(record_body, restored_executors)
            events, checkpoints = check_run_history(db)
            if events != manifest["runEventCount"] or checkpoints != manifest["runCheckpointCount"]:
                fail("restored event journal or checkpoints differ from manifest")
            if db.execute("SELECT 1 FROM research_requests WHERE state='claimed' UNION SELECT 1 FROM run_checkpoints WHERE active_claims_json<>'[]'").fetchone():
                fail("restored database kept live research claims")
        return
    except BaseException:
        for path in made:
            path.unlink(missing_ok=True)
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    backup = sub.add_parser("backup")
    backup.add_argument("--data-dir", required=True)
    backup.add_argument("--assets-root", required=True)
    backup.add_argument("--artifact-root", required=True)
    backup.add_argument("--out", required=True)
    backup.add_argument("--api-env", required=True)
    backup.add_argument("--ssh-key", required=True)
    backup.add_argument("--tls-key", required=True)
    verify = sub.add_parser("verify")
    verify.add_argument("--archive", required=True)
    verify.add_argument("--manifest-sha256", required=True)
    recover = sub.add_parser("restore")
    recover.add_argument("--archive", required=True)
    recover.add_argument("--manifest-sha256", required=True)
    recover.add_argument("--data-dir", required=True)
    recover.add_argument("--assets-root", required=True)
    recover.add_argument("--artifact-root", required=True)
    args = parser.parse_args()
    try:
        if args.command == "backup":
            known = secret_values_from_files(args.api_env, args.ssh_key, args.tls_key)
            print(create(args.data_dir, args.assets_root, args.artifact_root, args.out, known_secrets=known))
        elif args.command == "verify":
            verify_archive(Path(args.archive), args.manifest_sha256)
            print("verified")
        else:
            restore(args.archive, args.manifest_sha256, args.data_dir, args.assets_root, args.artifact_root)
            print("restored; owner account setup and Codex reconnection required")
    except OSError as exc:
        if exc.errno == errno.ENOSPC:
            raise SystemExit("recovery failed: storage exhausted: " + str(exc)) from None
        raise SystemExit("recovery failed: " + str(exc)) from None
    except (sqlite3.Error, ValueError, KeyError, json.JSONDecodeError) as exc:
        raise SystemExit("recovery failed: " + str(exc)) from None


if __name__ == "__main__":
    main()
