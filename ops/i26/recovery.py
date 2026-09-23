#!/usr/bin/env python3
"""Current-schema private SQLite and approved-asset recovery; no service control."""
import argparse
import base64
import hashlib
import json
import os
import pwd
import shutil
import sqlite3
import stat
import subprocess
import sys
import tempfile
from contextlib import closing
from datetime import datetime, timezone
from pathlib import Path

FORMAT = "jobseek-current-backup-v1"
DB_NAME = "jobseek.sqlite"
ASSETS = {
    "cv-vince-liem.typ": "e9643864392f2aff7f900a82714a8feb573f636c24c62e7a169c29de41bc9a57",
    "cv-vince-liem.md": "eaf82b8442ac51007b83397279d7f16e0f4a8547bd63f340253ad8263a9ddf61",
    "github-evidence-review.md": "4bf7467279e2440d7a1e870274726055bd1d33550e7272a7731aa568e0f6c4f3",
    "portfolio-case-studies.md": "67f5355ae303479a363b0040fe9b18732fce308612791f0666bbf7250a33d1c0",
}
SENSITIVE_COLUMNS = {
    ("administrator", "password_hash"), ("auth_sessions", "token_hash"), ("auth_sessions", "csrf_hash"),
    ("agent_credentials", "token_hash"), ("jobs", "lease_token"), ("job_attempts", "lease_token"),
    ("collector_boards", "lease_token"), ("round_tool_capabilities", "token_sha256"),
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
        db.execute("UPDATE collector_boards SET lease_token=NULL,lease_until=NULL")
        db.execute("DELETE FROM qualification_refresh_queue")
    check_db(db)
    if db.execute("SELECT 1 FROM administrator UNION SELECT 1 FROM auth_sessions UNION SELECT 1 FROM agent_credentials UNION SELECT 1 FROM round_tool_capabilities").fetchone():
        fail("credential rows survived sanitization")
    if db.execute("SELECT 1 FROM jobs WHERE state IN ('queued','running') UNION SELECT 1 FROM rounds WHERE state IN ('queued','running','awaiting_input','stopping','paused') UNION SELECT 1 FROM collector_boards WHERE lease_token IS NOT NULL OR lease_until IS NOT NULL UNION SELECT 1 FROM delivery_items WHERE state='sending'").fetchone():
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


def verify_archive(archive, expected_manifest_sha, approved=ASSETS):
    checked_path(archive, directory=True)
    allowed = {"manifest.json", DB_NAME, "assets"}
    if {p.name for p in archive.iterdir()} != allowed:
        fail("archive has missing or extra entries")
    manifest_path = archive / "manifest.json"
    db_path = archive / DB_NAME
    private_file(manifest_path)
    private_file(db_path)
    if file_digest(manifest_path) != expected_manifest_sha:
        fail("backup manifest hash differs from the separately recorded pin")
    manifest = json.loads(manifest_path.read_bytes())
    if manifest.get("format") != FORMAT or manifest.get("files") is None or manifest.get("schemaSha256") is None:
        fail("backup manifest is not current format")
    if set(manifest["files"]) != {DB_NAME} | {"assets/" + name for name in approved}:
        fail("backup file list differs from current approved assets")
    for name, sha in manifest["files"].items():
        path = archive / name
        private_file(path)
        if file_digest(path) != sha:
            fail("backup file hash mismatch")
    assets_dir = checked_path(archive / "assets", directory=True)
    assets = asset_bytes(assets_dir, approved)
    with closing(open_ro(db_path)) as db:
        schema = check_db(db)
        packs = check_packs(db, assets)
        check_delivery_material(db)
        if schema != manifest["schemaSha256"] or packs != manifest.get("packCount"):
            fail("backup schema or pack count differs from manifest")
        if db.execute("SELECT 1 FROM administrator UNION SELECT 1 FROM auth_sessions UNION SELECT 1 FROM agent_credentials UNION SELECT 1 FROM round_tool_capabilities").fetchone():
            fail("backup contains credential rows")
        if db.execute("SELECT 1 FROM jobs WHERE state IN ('queued','running') UNION SELECT 1 FROM rounds WHERE state IN ('queued','running','awaiting_input','stopping','paused') UNION SELECT 1 FROM collector_boards WHERE lease_token IS NOT NULL OR lease_until IS NOT NULL UNION SELECT 1 FROM delivery_items WHERE state='sending'").fetchone():
            fail("backup contains runnable recruitment state")
    return manifest


def create(data_dir, assets_root, output, approved=ASSETS, known_secrets=()):
    data_dir = checked_path(data_dir, directory=True)
    assets_root = checked_path(assets_root, directory=True, group_read=True)
    output = checked_path(output, exists=False)
    checked_path(output.parent, directory=True)
    if ROOT in output.parents or data_dir in output.parents or assets_root in output.parents:
        fail("backup destination must be outside source, data and asset directories")
    source = data_dir / DB_NAME
    private_file(source)
    assets = asset_bytes(assets_root, approved)
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
                sanitize(db)
                final = output / DB_NAME
                db.execute("VACUUM INTO ?", (str(final),))
            os.chmod(final, 0o600)
        asset_out = output / "assets"
        asset_out.mkdir(mode=0o700)
        for name, body in assets.items():
            path = asset_out / name
            path.write_bytes(body)
            os.chmod(path, 0o600)
        with closing(open_ro(final)) as db:
            check_db(db)
            count = check_packs(db, assets)
            check_delivery_material(db)
        files = {DB_NAME: file_digest(final)} | {"assets/" + name: digest(body) for name, body in assets.items()}
        manifest = {"format": FORMAT, "createdAt": datetime.now(timezone.utc).isoformat(), "schemaSha256": schema, "packCount": count, "files": files}
        manifest_path = output / "manifest.json"
        manifest_path.write_bytes(json.dumps(manifest, sort_keys=True, separators=(",", ":")).encode() + b"\n")
        os.chmod(manifest_path, 0o600)
        reject_secret_bytes([final, manifest_path, *(asset_out / name for name in assets)], known_secrets)
        manifest_sha = file_digest(manifest_path)
        verify_archive(output, manifest_sha, approved)
        return manifest_sha
    except BaseException:
        shutil.rmtree(output, ignore_errors=True)
        raise


def restore(archive, manifest_sha, data_dir, assets_root, approved=ASSETS):
    archive = checked_path(archive, directory=True)
    verify_archive(archive, manifest_sha, approved)
    data_dir = checked_path(data_dir, directory=True)
    assets_root = checked_path(assets_root, directory=True, group_read=True)
    if (data_dir / DB_NAME).exists() or (data_dir / (DB_NAME + "-wal")).exists() or (data_dir / (DB_NAME + "-shm")).exists():
        fail("restore target already contains SQLite state")
    if any(data_dir.iterdir()):
        fail("restore target data directory must be empty")
    current = {p.name for p in assets_root.iterdir()}
    if current and current != set(approved):
        fail("restore target assets are neither empty nor the pinned set")
    if current:
        asset_bytes(assets_root, approved)
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
        with closing(open_ro(dest)) as db:
            check_db(db)
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
    args = parser.parse_args()
    try:
        if args.command == "backup":
            known = secret_values_from_files(args.api_env, args.ssh_key, args.tls_key)
            print(create(args.data_dir, args.assets_root, args.out, known_secrets=known))
        elif args.command == "verify":
            verify_archive(Path(args.archive), args.manifest_sha256)
            print("verified")
        else:
            restore(args.archive, args.manifest_sha256, args.data_dir, args.assets_root)
            print("restored; owner account setup and Codex reconnection required")
    except (OSError, sqlite3.Error, ValueError, KeyError, json.JSONDecodeError) as exc:
        raise SystemExit("recovery failed: " + str(exc)) from None


if __name__ == "__main__":
    main()
