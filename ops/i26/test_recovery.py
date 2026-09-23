import importlib.util
import json
import re
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from contextlib import closing
from pathlib import Path

import recovery


CANARY = b"SYNTHETIC-CREDENTIAL-CANARY-DO-NOT-EXPORT"
NOW = "2026-01-01T00:00:00Z"


def private_dir(parent, name):
    path = parent / name
    path.mkdir(mode=0o700)
    return path


class RecoveryTest(unittest.TestCase):
    def test_schema_bootstrap_matches_go_store(self):
        store = (recovery.ROOT / "apps/api/internal/store/store.go").read_text()
        match = re.search(r'tx\.ExecContext\(ctx, `(CREATE TABLE IF NOT EXISTS schema_migrations \([\s\S]*?\))`\)', store)
        self.assertIsNotNone(match)
        self.assertEqual(recovery.MIGRATION_TABLE_SQL, match.group(1))

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.data = private_dir(self.root, "data")
        self.assets = private_dir(self.root, "assets")
        self.backups = private_dir(self.root, "backups")
        self.approved = {name: ("synthetic " + name + "\n").encode() for name in recovery.ASSETS}
        self.digests = {name: recovery.digest(body) for name, body in self.approved.items()}
        for name, body in self.approved.items():
            path = self.assets / name
            path.write_bytes(body)
            path.chmod(0o600)
        db_path = self.data / recovery.DB_NAME
        with closing(sqlite3.connect(db_path)) as db, db:
            db.execute("PRAGMA foreign_keys=ON")
            db.execute(recovery.MIGRATION_TABLE_SQL)
            for version, name, sha, sql in recovery.source_migrations():
                db.executescript(sql)
                db.execute("INSERT INTO schema_migrations VALUES (?,?,?,?)", (version, name, sha, NOW))
            db.execute("INSERT INTO preferences_versions VALUES (1,'Brussels',1,1,4000,0,'EUR','{}','Europe/Brussels',?,'administrator','owner')", (NOW,))
            db.execute("INSERT INTO preferences_current VALUES (1,1)")
            db.execute("INSERT INTO companies(id,name,created_at,updated_at) VALUES ('company-1','Synthetic employer',?,?)", (NOW, NOW))
            db.execute("INSERT INTO opportunities(id,company_id,title,kind,stage,created_at,updated_at) VALUES ('role-1','company-1','Synthetic role','employment','discovered',?,?)", (NOW, NOW))
            manifest = json.dumps({"role": {"opportunityId": "role-1", "opportunityRevision": 1, "profileRevision": 1}, "sources": [{"id": name, "sha256": self.digests[name], "body": body.decode(), "approved": True} for name, body in self.approved.items()]}, separators=(",", ":")).encode()
            source = b"#let synthetic = true\n"
            pdf = b"%PDF-1.7\n" + b"synthetic fixture\n" * 10
            self.pdf = pdf
            db.execute("INSERT INTO application_packs VALUES (?,?,?,?,?,?,?,?,?,?)", ("pack-1", "role-1", 1, 1, 1, recovery.pack_hash(manifest, source, pdf), manifest, source, pdf, NOW))
            db.execute("INSERT INTO administrator VALUES (1,?,1,?,?)", (CANARY.decode(), NOW, NOW))
            db.execute("INSERT INTO auth_sessions(token_hash,csrf_hash,created_at,expires_at) VALUES (?,?,?,?)", (CANARY.decode() + "-session", CANARY.decode() + "-csrf", NOW, NOW))
            db.execute("INSERT INTO agent_credentials(id,name,token_hash,scopes_json,created_at,expires_at) VALUES ('agent-1','synthetic',?,'[]',?,?)", (CANARY.decode() + "-agent", NOW, NOW))
            db.execute("INSERT INTO jobs(id,kind,payload_json,payload_sha256,actor_kind,actor_id,idempotency_key,request_sha256,state,max_attempts,available_at,lease_token,lease_owner,lease_until,created_at,updated_at) VALUES ('job-1','synthetic','{}',?,'administrator','owner','job-key',?,'running',3,?,?,?,?,?,?)", ("a" * 64, "b" * 64, NOW, CANARY.decode() + "-lease", "worker-1", NOW, NOW, NOW))
            db.execute("INSERT INTO job_attempts(job_id,attempt_no,lease_token,worker_id,started_at,lease_until,outcome) VALUES ('job-1',1,?,'worker-1',?,?,'running')", (CANARY.decode() + "-attempt", NOW, NOW))
            db.execute("INSERT INTO collector_boards(id,provider,site,region,display_name,official_careers_url,verified_at,enabled,interval_minutes,next_scan_at,lease_token,lease_until,created_at,updated_at) VALUES ('board-1','lever','synthetic','global','Synthetic board','https://jobs.lever.co/synthetic',?,1,60,?,?,?, ?,?)", (NOW, NOW, CANARY.decode() + "-board-lease", NOW, NOW, NOW))
            db.execute("INSERT INTO rounds(id,actor_kind,actor_id,request_key,request_sha256,intent,outcome,profile_version,scope_json,state,revision,generation,deadline_at,request_limit,item_limit,tool_limit,turn_limit,created_at,updated_at) VALUES ('round-1','administrator','owner','round-key',?,'Synthetic check','discover',1,'{}','running',1,1,?,2,2,2,2,?,?)", ("c" * 64, NOW, NOW, NOW))
            db.execute("INSERT INTO round_attempts(id,round_id,request_key,request_sha256,operation,resource_id,generation,state,requests_reserved,items_reserved,tools_reserved,turns_reserved,created_at,updated_at) VALUES ('attempt-1','round-1','turn-key',?,'codex.turn','company:company-1',1,'dispatched',0,0,1,1,?,?)", ("d" * 64, NOW, NOW))
            db.execute("INSERT INTO round_tool_capabilities VALUES (?,?,?,?,?,?,NULL)", (CANARY.decode() + "-capability", "round-1", "attempt-1", "agent-1", 1, NOW))
            db.execute("INSERT INTO round_remote_dispatches(attempt_id,round_id,generation,thread_id,turn_id,updated_at) VALUES ('attempt-1','round-1',1,'thread-1','turn-1',?)", (NOW,))
            db.execute("INSERT INTO jev_attempts(id,round_id,round_attempt_id,step_index,purpose,input_sha256,source_refs_json,candidate_set_json,profile_version,rubric_version,logical_request_json,raw_response_bytes,status,created_at) VALUES ('jev-1','round-1','attempt-1',0,'screening',?,'[]','[]',1,'current','{}',?,'dispatched',?)", ("e" * 64, b"synthetic Jev evidence", NOW))
            db.execute("INSERT INTO relationship_counterparties(id,display_name,kind,organization_text,source_kind,source_excerpt,observed_at,created_at,updated_at) VALUES ('person-1','Synthetic contact','contact','Synthetic org','owner','Synthetic source',?,?,?)", (NOW, NOW, NOW))
            db.execute("INSERT INTO relationship_events(id,counterparty_id,opportunity_id,kind,summary,source_kind,source_excerpt,observed_at,created_at,updated_at) VALUES ('event-1','person-1','role-1','conversation','Synthetic conversation','owner','Synthetic excerpt',?,?,?)", (NOW, NOW, NOW))
            db.execute("PRAGMA secure_delete=OFF")
            db.execute("UPDATE opportunities SET notes=? WHERE id='role-1'", (CANARY.decode() * 200,))
            db.execute("UPDATE opportunities SET notes='' WHERE id='role-1'")
        db_path.chmod(0o600)

    def test_round_trip_sanitizes_credentials_and_preserves_pack_history(self):
        archive = self.backups / "archive"
        pin = recovery.create(self.data, self.assets, archive, self.digests)
        self.assertEqual(recovery.verify_archive(archive, pin, self.digests)["packCount"], 1)
        self.assertNotIn(CANARY, (archive / recovery.DB_NAME).read_bytes())
        self.assertEqual({p.name for p in archive.iterdir()}, {"jobseek.sqlite", "assets", "manifest.json"})
        restored_data = private_dir(self.root, "restored-data")
        restored_assets = private_dir(self.root, "restored-assets")
        recovery.restore(archive, pin, restored_data, restored_assets, self.digests)
        self.assertEqual((restored_assets / "cv-vince-liem.typ").read_bytes(), self.approved["cv-vince-liem.typ"])
        with closing(sqlite3.connect(restored_data / recovery.DB_NAME)) as db:
            self.assertEqual(db.execute("SELECT pdf FROM application_packs WHERE id='pack-1'").fetchone()[0], self.pdf)
            self.assertEqual(db.execute("SELECT state,reconciliation_required FROM rounds WHERE id='round-1'").fetchone(), ("failed", 1))
            self.assertEqual(db.execute("SELECT state FROM round_attempts WHERE id='attempt-1'").fetchone()[0], "uncertain")
            self.assertEqual(db.execute("SELECT thread_id,turn_id FROM round_remote_dispatches").fetchone(), ("thread-1", "turn-1"))
            self.assertEqual(db.execute("SELECT status FROM jev_attempts").fetchone()[0], "uncertain")
            self.assertEqual(db.execute("SELECT raw_response_bytes FROM jev_attempts").fetchone()[0], b"synthetic Jev evidence")
            self.assertEqual(db.execute("SELECT state,lease_token FROM jobs").fetchone(), ("failed", None))
            self.assertEqual(db.execute("SELECT enabled,lease_token,lease_until FROM collector_boards WHERE id='board-1'").fetchone(), (1, None, None))
            self.assertEqual(db.execute("SELECT count(*) FROM jobs WHERE state IN ('queued','running')").fetchone()[0], 0)
            self.assertEqual(db.execute("SELECT count(*) FROM rounds WHERE state IN ('queued','running','awaiting_input','stopping','paused')").fetchone()[0], 0)
            self.assertEqual(db.execute("SELECT id FROM collector_boards WHERE enabled=1 AND verified_at IS NOT NULL AND verified_at<>''").fetchone()[0], "board-1")
            self.assertEqual(db.execute("SELECT display_name FROM relationship_counterparties").fetchone()[0], "Synthetic contact")
            self.assertEqual(db.execute("SELECT summary FROM relationship_events").fetchone()[0], "Synthetic conversation")
            self.assertEqual(db.execute("SELECT count(*) FROM administrator").fetchone()[0], 0)
            self.assertEqual(db.execute("SELECT count(*) FROM auth_sessions").fetchone()[0], 0)
            self.assertEqual(db.execute("SELECT count(*) FROM agent_credentials").fetchone()[0], 0)
            self.assertEqual(db.execute("SELECT count(*) FROM round_tool_capabilities").fetchone()[0], 0)

    def test_corruption_and_unexpected_assets_are_rejected(self):
        archive = self.backups / "archive"
        pin = recovery.create(self.data, self.assets, archive, self.digests)
        with (archive / recovery.DB_NAME).open("r+b") as f:
            f.seek(100)
            f.write(b"BROKEN")
        with self.assertRaises(ValueError):
            recovery.verify_archive(archive, pin, self.digests)
        with self.assertRaises(ValueError):
            recovery.restore(archive, pin, private_dir(self.root, "target-data"), private_dir(self.root, "target-assets"), self.digests)
        second = self.backups / "second"
        pin2 = recovery.create(self.data, self.assets, second, self.digests)
        (second / "assets" / "cv-vince-liem.typ").write_bytes(b"changed")
        with self.assertRaises(ValueError):
            recovery.verify_archive(second, pin2, self.digests)

    def test_known_secret_in_retained_text_aborts_backup(self):
        with closing(sqlite3.connect(self.data / recovery.DB_NAME)) as db, db:
            db.execute("UPDATE opportunities SET notes=? WHERE id='role-1'", (CANARY.decode(),))
        archive = self.backups / "leaky"
        with self.assertRaisesRegex(ValueError, "known deployment secret"):
            recovery.create(self.data, self.assets, archive, self.digests, [CANARY])
        self.assertFalse(archive.exists())

    def test_packaged_script_works_without_source_checkout(self):
        bundle = self.root / "bundle"
        subprocess.run([str(recovery.ROOT / "ops/i26/build-recovery.sh"), str(bundle)], check=True, capture_output=True)
        spec = importlib.util.spec_from_file_location("bundled_recovery", bundle / "recovery.py")
        packaged = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(packaged)
        self.assertEqual(packaged.schema_spec()["schemaSha256"], recovery.schema_spec()["schemaSha256"])
        archive = self.backups / "packaged"
        pin = packaged.create(self.data, self.assets, archive, self.digests, [CANARY])
        self.assertEqual(len(pin), 64)
        packaged.verify_archive(archive, pin, self.digests)
        subprocess.run([sys.executable, str(bundle / "recovery.py"), "--help"], check=True, capture_output=True)


if __name__ == "__main__":
    unittest.main()
