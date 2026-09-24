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
from unittest import mock

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
        self.artifacts = private_dir(self.root, "artifacts")
        self.blob_fetch = b"synthetic fetched vacancy page\n"
        self.blob_search = b"synthetic search snippet\n"
        self.sha_fetch = recovery.digest(self.blob_fetch)
        self.sha_search = recovery.digest(self.blob_search)
        self.ref_fetch = "blobs/" + self.sha_fetch[:2] + "/" + self.sha_fetch
        self.ref_search = "blobs/" + self.sha_search[:2] + "/" + self.sha_search
        for ref, body in ((self.ref_fetch, self.blob_fetch), (self.ref_search, self.blob_search)):
            path = self.artifacts / ref
            path.parent.mkdir(parents=True, mode=0o700)
            path.write_bytes(body)
            path.chmod(0o600)
        (self.artifacts / "receipts").mkdir(mode=0o700)
        self.fp_claimed = recovery.digest(b"fixture-request-claimed")
        self.fp_fresh = recovery.digest(b"fixture-request-fresh")
        self.fp_uncertain = recovery.digest(b"fixture-request-uncertain")
        self.exec_fetch = {"backend": "fixture-backend", "version": "fixture-1", "digest": "sha256:fixture"}
        self.exec_browse = {"backend": "fixture-browser", "version": "fixture-2", "digest": "sha256:browser"}
        for rid, fp, status, cid in (("rcpt-1", self.fp_fresh, "ok", self.sha_fetch),
                                     ("rcpt-2", self.fp_uncertain, "uncertain", self.sha_search)):
            body = json.dumps({"id": rid, "operation": "fetch", "fingerprint": fp, "status": status,
                               "captureId": cid, "attempts": 1}, separators=(",", ":")).encode()
            path = self.artifacts / "receipts" / (rid + ".json")
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
            db.execute("INSERT INTO rounds(id,actor_kind,actor_id,request_key,request_sha256,intent,outcome,initial_profile_version,profile_version,scope_json,state,revision,generation,deadline_at,request_limit,item_limit,tool_limit,turn_limit,created_at,updated_at) VALUES ('round-1','administrator','owner','round-key',?,'Synthetic delivery','deliver',1,1,'{}','running',1,1,?,2,2,2,2,?,?)", ("c" * 64, NOW, NOW, NOW))
            for round_id, outcome in (("round-interview", "interview_prepare"), ("round-offer", "compare_offers"), ("round-route", "delivery_route_assess")):
                db.execute("INSERT INTO rounds(id,actor_kind,actor_id,request_key,request_sha256,intent,outcome,initial_profile_version,profile_version,scope_json,state,revision,generation,deadline_at,request_limit,item_limit,tool_limit,turn_limit,created_at,updated_at,completed_at) VALUES (?,?,?,?,?,'Synthetic saved result',?,1,1,'{}','completed',2,1,?,2,2,2,2,?,?,?)", (round_id, "administrator", "owner", round_id, "f" * 64, outcome, NOW, NOW, NOW, NOW))
            db.execute("INSERT INTO round_attempts(id,round_id,request_key,request_sha256,operation,resource_id,generation,state,requests_reserved,items_reserved,tools_reserved,turns_reserved,created_at,updated_at) VALUES ('attempt-1','round-1','turn-key',?,'codex.turn','company:company-1',1,'dispatched',0,0,1,1,?,?)", ("d" * 64, NOW, NOW))
            db.execute("INSERT INTO round_tool_capabilities VALUES (?,?,?,?,?,?,NULL)", (CANARY.decode() + "-capability", "round-1", "attempt-1", "agent-1", 1, NOW))
            db.execute("INSERT INTO round_remote_dispatches(attempt_id,round_id,generation,thread_id,turn_id,updated_at) VALUES ('attempt-1','round-1',1,'thread-1','turn-1',?)", (NOW,))
            db.execute("INSERT INTO jev_attempts(id,round_id,round_attempt_id,step_index,purpose,input_sha256,source_refs_json,candidate_set_json,profile_version,rubric_version,logical_request_json,raw_response_bytes,status,created_at) VALUES ('jev-1','round-1','attempt-1',0,'screening',?,'[]','[]',1,'current','{}',?,'dispatched',?)", ("e" * 64, b"synthetic Jev evidence", NOW))
            for suffix, round_id, purpose in (("route", "round-route", "delivery_route"), ("offer", "round-offer", "offer_tradeoff")):
                db.execute("INSERT INTO round_attempts(id,round_id,request_key,request_sha256,operation,resource_id,generation,state,requests_reserved,items_reserved,tools_reserved,turns_reserved,created_at,updated_at,finished_at) VALUES (?,?,?,?,?,'role-1',1,'succeeded',1,0,0,0,?,?,?)", ("attempt-" + suffix, round_id, "key-" + suffix, "6" * 64, "jev.request", NOW, NOW, NOW))
                db.execute("INSERT INTO jev_attempts(id,round_id,round_attempt_id,step_index,purpose,input_sha256,source_refs_json,candidate_set_json,profile_version,rubric_version,logical_request_json,raw_response_bytes,status,created_at,finished_at) VALUES (?,?,?,?,?,?,'[]','[]',1,'current','{}',?,'succeeded',?,?)", ("jev-" + suffix, round_id, "attempt-" + suffix, 0, purpose, "7" * 64, b"synthetic saved choice", NOW, NOW))
            db.execute("INSERT INTO opportunity_routes(id,opportunity_id,kind,destination_text,source_kind,source_excerpt,observed_at,created_at,updated_at) VALUES ('route-1','role-1','direct','apply@example.invalid','original_opening','Apply by email to apply@example.invalid',?,?,?)", (NOW, NOW, NOW))
            db.execute("INSERT INTO delivery_route_assessments(id,opportunity_id,route_id,source_sha256,route_sha256,input_sha256,choice,round_id,jev_attempt_id,result_json,created_at) VALUES ('route-assessment-1','role-1','route-1',?,?,?,'application_mailbox','round-route','jev-route',?,?)", ("8" * 64, "9" * 64, "a" * 64, '{"choice":"application_mailbox"}', NOW))
            db.execute("INSERT INTO delivery_reviews(id,owner_id,request_key,pack_ids_json,material_sha256,approved_sha256,approved_at,created_at) VALUES ('review-1','owner','delivery-review-key','[\"pack-1\"]',?,?,?,?)", ("a" * 64, "a" * 64, NOW, NOW))
            self.mime = b"From: sender@example.invalid\r\nTo: apply@example.invalid\r\nSubject: Synthetic application\r\n\r\nSynthetic body\r\n"
            db.execute("""INSERT INTO delivery_items(id,review_id,pack_id,opportunity_id,opportunity_revision,source_sha256,profile_revision,pack_content_sha256,route_id,route_revision,route_sha256,title,company_name,route_excerpt,recipient,sender,subject,body,attachment_sha256,mime_sha256,mime_bytes,message_id,state,round_id,attempt_id,created_at,updated_at)
                          VALUES ('delivery-1','review-1','pack-1','role-1',1,?,1,?,'route-1',1,?,'Synthetic role','Synthetic employer','Apply by email to apply@example.invalid','apply@example.invalid','sender@example.invalid','Synthetic application','Synthetic body',?,?,?,'<synthetic@example.invalid>','sending','round-1','attempt-1',?,?)""", ("b" * 64, recovery.pack_hash(manifest, source, pdf), "d" * 64, recovery.digest(pdf), recovery.digest(self.mime), self.mime, NOW, NOW))
            self.interview_context = "Complete synthetic invitation and role context"
            self.interview_brief = json.dumps({"input": {"sources": [{"name": "invitation", "body": self.interview_context}]}, "summary": "Synthetic prepared brief"}, separators=(",", ":"))
            db.execute("INSERT INTO interviews(id,opportunity_id,opportunity_revision,profile_version,actor_id,request_key,request_sha256,context_text,context_sha256,round_id,brief_json,brief_sha256,focus_json,created_at,updated_at) VALUES ('interview-1','role-1',1,1,'owner','interview-key',?,?,?,?,? ,?,?,?,?)", ("1" * 64, self.interview_context, recovery.digest(self.interview_context.encode()), "round-interview", self.interview_brief, "2" * 64, '{"selection":{"disposition":"selected","selected_id":"focus-1"}}', NOW, NOW))
            self.debrief_notes = "Synthetic owner reported interview observations"
            db.execute("INSERT INTO interview_debriefs(id,interview_id,actor_id,request_key,request_sha256,owner_notes,round_id,debrief_json,created_at,updated_at) VALUES ('debrief-1','interview-1','owner','debrief-key',?,?,NULL,?,?,?)", ("3" * 64, self.debrief_notes, '{"attribution":"owner_reported"}', NOW, NOW))
            self.offer_sources = '[{"label":"Offer A","text":"Synthetic complete offer"}]'
            self.offer_result = '{"input":{"sources":[{"label":"Offer A"}]},"pay":{"exact":"9007199254740993"}}'
            db.execute("INSERT INTO offer_comparison_intakes(id,owner_id,request_key,request_sha256,sources_json,created_at) VALUES ('intake-1','owner','offer-key',?,?,?)", ("4" * 64, self.offer_sources, NOW))
            db.execute("INSERT INTO offer_comparisons(id,intake_id,round_id,input_sha256,comparison_json,created_at) VALUES ('comparison-1','intake-1','round-offer',?,?,?)", ("5" * 64, self.offer_result, NOW))
            db.execute("INSERT INTO offer_tradeoff_assessments(id,comparison_id,round_id,jev_attempt_id,result_json,created_at) VALUES ('tradeoff-1','comparison-1','round-offer','jev-offer',?,?)", ('{"selection":"Offer A"}', NOW))
            db.execute("INSERT INTO relationship_counterparties(id,display_name,kind,organization_text,source_kind,source_excerpt,observed_at,created_at,updated_at) VALUES ('person-1','Synthetic contact','contact','Synthetic org','owner','Synthetic source',?,?,?)", (NOW, NOW, NOW))
            db.execute("INSERT INTO relationship_events(id,counterparty_id,opportunity_id,kind,summary,source_kind,source_excerpt,observed_at,created_at,updated_at) VALUES ('event-1','person-1','role-1','conversation','Synthetic conversation','owner','Synthetic excerpt',?,?,?)", (NOW, NOW, NOW))
            fetch_exec = json.dumps(self.exec_fetch, separators=(",", ":"))
            browse_exec = json.dumps(self.exec_browse, separators=(",", ":"))
            db.execute("INSERT INTO source_captures VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                       ("cap-1", self.sha_fetch, self.ref_fetch, len(self.blob_fetch), "text/html", 200,
                        "https://example.invalid/jobs/42", "https://example.invalid/jobs/42", "[]", NOW,
                        "fetched_response", "complete", "{}", fetch_exec, None, 0, NOW))
            db.execute("INSERT INTO source_captures VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                       ("cap-2", self.sha_search, self.ref_search, len(self.blob_search), "application/json", None,
                        "https://example.invalid/search?q=synthetic", None, "[]", NOW,
                        "search_result", "complete", "{}", browse_exec, None, 1, NOW))
            db.execute("INSERT INTO source_captures VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                       ("cap-3", self.sha_fetch, self.ref_fetch, len(self.blob_fetch), "text/html", 200,
                        "https://example.invalid/jobs/42", "https://example.invalid/jobs/42", "[]", NOW,
                        "fetched_response", "complete", "{}", fetch_exec, None, 0, NOW))
            db.execute("INSERT INTO research_requests VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                       ("req-claimed", "administrator", "owner", self.fp_claimed, '{"operation":"fetch"}',
                        "stateless_reusable", "claimed", "attempt-1", 1, "2026-01-02T00:00:00Z",
                        None, None, None, None, None, NOW, NOW))
            db.execute("INSERT INTO research_requests VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                       ("req-fresh", "administrator", "owner", self.fp_fresh, '{"operation":"fetch"}',
                        "stateless_reusable", "fresh", None, None, None,
                        None, None, "2026-01-02T00:00:00Z", None, None, NOW, NOW))
            db.execute("INSERT INTO research_requests VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                       ("req-uncertain", "administrator", "owner", self.fp_uncertain, '{"operation":"fetch"}',
                        "stateless_reusable", "uncertain", None, None, None,
                        None, None, None, None, None, NOW, NOW))
            db.execute("INSERT INTO research_observations VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                       ("obs-1", "req-fresh", 1, "administrator", "owner", "round-1", "attempt-1",
                        "fetch", "https://example.invalid/jobs/42", None, NOW, NOW, "success",
                        "fetched_response", "rcpt-1", fetch_exec, "cap-1", "", "", 0, NOW))
            db.execute("INSERT INTO research_observations VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                       ("obs-2", "req-uncertain", 1, "administrator", "owner", "round-1", "attempt-1",
                        "fetch", "https://example.invalid/search?q=synthetic", None, NOW, NOW, "uncertain",
                        "search_result", "rcpt-2", browse_exec, "cap-2", "", "", 0, NOW))
            db.execute("INSERT INTO research_observations VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                       ("obs-3", "req-fresh", 2, "administrator", "owner", "round-1", None,
                        "fetch", "https://example.invalid/jobs/42", None, NOW, NOW, "late",
                        "fetched_response", None, None, "cap-1", "", "", 1, NOW))
            db.execute("UPDATE research_requests SET latest_observation_id='obs-1',latest_capture_id='cap-1' WHERE id='req-fresh'")
            db.execute("UPDATE research_requests SET latest_observation_id='obs-2',latest_capture_id='cap-2' WHERE id='req-uncertain'")
            db.execute("INSERT INTO run_events VALUES (?,?,?,?,?,?,?,?,?,?,?)",
                       ("ev-claim", "round-1", "attempt-1", "claim", self.fp_fresh, None, None, "ok", None, NOW, NOW))
            db.execute("INSERT INTO run_events VALUES (?,?,?,?,?,?,?,?,?,?,?)",
                       ("ev-obs", "round-1", "attempt-1", "observation", self.fp_fresh, "obs-1", "cap-1", "ok", None, NOW, NOW))
            db.execute("INSERT INTO run_events VALUES (?,?,?,?,?,?,?,?,?,?,?)",
                       ("ev-uncertain", "round-1", "attempt-1", "observation", self.fp_uncertain, "obs-2", "cap-2", "outcome_uncertain", None, NOW, NOW))
            self.active_claims = json.dumps([{"fingerprint": self.fp_claimed, "leaseUntil": "2026-01-02T00:00:00Z"}])
            db.execute("INSERT INTO run_checkpoints VALUES (?,?,?,?,?,?,?,?,?,?,?)",
                       ("round-1", 1, "current", self.active_claims, '["cap-1","cap-2"]', '[]',
                        '[{"attempt":"attempt-1","reason":"uncertain"}]', None, '[{"op":"fetch"}]', 1, NOW))
            db.execute("PRAGMA secure_delete=OFF")
            db.execute("UPDATE opportunities SET notes=? WHERE id='role-1'", (CANARY.decode() * 200,))
            db.execute("UPDATE opportunities SET notes='' WHERE id='role-1'")
        db_path.chmod(0o600)

    def test_round_trip_sanitizes_credentials_and_preserves_pack_history(self):
        archive = self.backups / "archive"
        pin = recovery.create(self.data, self.assets, self.artifacts, archive, self.digests)
        self.assertEqual(recovery.verify_archive(archive, pin, self.digests)["packCount"], 1)
        self.assertNotIn(CANARY, (archive / recovery.DB_NAME).read_bytes())
        self.assertEqual({p.name for p in archive.iterdir()}, {"jobseek.sqlite", "assets", "manifest.json", "captures", "executor-identity.json"})
        restored_data = private_dir(self.root, "restored-data")
        restored_assets = private_dir(self.root, "restored-assets")
        restored_artifacts = private_dir(self.root, "restored-artifacts")
        recovery.restore(archive, pin, restored_data, restored_assets, restored_artifacts, self.digests)
        self.assertEqual((restored_assets / "cv-vince-liem.typ").read_bytes(), self.approved["cv-vince-liem.typ"])
        with closing(sqlite3.connect(restored_data / recovery.DB_NAME)) as db:
            self.assertEqual(db.execute("SELECT pdf FROM application_packs WHERE id='pack-1'").fetchone()[0], self.pdf)
            self.assertEqual(db.execute("SELECT state,reconciliation_required FROM rounds WHERE id='round-1'").fetchone(), ("failed", 1))
            self.assertEqual(db.execute("SELECT state FROM round_attempts WHERE id='attempt-1'").fetchone()[0], "uncertain")
            self.assertEqual(db.execute("SELECT thread_id,turn_id FROM round_remote_dispatches").fetchone(), ("thread-1", "turn-1"))
            self.assertEqual(db.execute("SELECT status FROM jev_attempts").fetchone()[0], "uncertain")
            self.assertEqual(db.execute("SELECT raw_response_bytes FROM jev_attempts").fetchone()[0], b"synthetic Jev evidence")
            self.assertEqual(db.execute("SELECT state,lease_token FROM jobs").fetchone(), ("failed", None))
            self.assertEqual(db.execute("SELECT count(*) FROM jobs WHERE state IN ('queued','running')").fetchone()[0], 0)
            self.assertEqual(db.execute("SELECT count(*) FROM rounds WHERE state IN ('queued','running','awaiting_input','stopping','paused')").fetchone()[0], 0)
            self.assertEqual(db.execute("SELECT display_name FROM relationship_counterparties").fetchone()[0], "Synthetic contact")
            self.assertEqual(db.execute("SELECT summary FROM relationship_events").fetchone()[0], "Synthetic conversation")
            self.assertEqual(db.execute("SELECT context_text,brief_json,focus_json FROM interviews WHERE id='interview-1'").fetchone(), (self.interview_context, self.interview_brief, '{"selection":{"disposition":"selected","selected_id":"focus-1"}}'))
            self.assertEqual(db.execute("SELECT owner_notes,debrief_json FROM interview_debriefs WHERE id='debrief-1'").fetchone(), (self.debrief_notes, '{"attribution":"owner_reported"}'))
            self.assertEqual(db.execute("SELECT sources_json FROM offer_comparison_intakes WHERE id='intake-1'").fetchone()[0], self.offer_sources)
            self.assertEqual(db.execute("SELECT comparison_json FROM offer_comparisons WHERE id='comparison-1'").fetchone()[0], self.offer_result)
            self.assertEqual(db.execute("SELECT result_json FROM offer_tradeoff_assessments WHERE id='tradeoff-1'").fetchone()[0], '{"selection":"Offer A"}')
            self.assertEqual(db.execute("SELECT pack_ids_json,approved_sha256 FROM delivery_reviews WHERE id='review-1'").fetchone(), ('["pack-1"]', "a" * 64))
            self.assertEqual(db.execute("SELECT state,smtp_stage,smtp_code,round_id,attempt_id,mime_bytes FROM delivery_items WHERE id='delivery-1'").fetchone(), ("uncertain", "interrupted", 0, "round-1", "attempt-1", self.mime))
            self.assertEqual(db.execute("SELECT destination_text,source_excerpt FROM opportunity_routes WHERE id='route-1'").fetchone(), ("apply@example.invalid", "Apply by email to apply@example.invalid"))
            self.assertEqual(db.execute("SELECT choice,result_json FROM delivery_route_assessments WHERE id='route-assessment-1'").fetchone(), ("application_mailbox", '{"choice":"application_mailbox"}'))
            self.assertEqual(db.execute("SELECT count(*) FROM delivery_items WHERE state='sending'").fetchone()[0], 0)
            self.assertEqual(db.execute("SELECT count(*) FROM administrator").fetchone()[0], 0)
            self.assertEqual(db.execute("SELECT count(*) FROM auth_sessions").fetchone()[0], 0)
            self.assertEqual(db.execute("SELECT count(*) FROM agent_credentials").fetchone()[0], 0)
            self.assertEqual(db.execute("SELECT count(*) FROM round_tool_capabilities").fetchone()[0], 0)

    def test_corruption_and_unexpected_assets_are_rejected(self):
        archive = self.backups / "archive"
        pin = recovery.create(self.data, self.assets, self.artifacts, archive, self.digests)
        with (archive / recovery.DB_NAME).open("r+b") as f:
            f.seek(100)
            f.write(b"BROKEN")
        with self.assertRaises(ValueError):
            recovery.verify_archive(archive, pin, self.digests)
        with self.assertRaises(ValueError):
            recovery.restore(archive, pin, private_dir(self.root, "target-data"), private_dir(self.root, "target-assets"), private_dir(self.root, "target-artifacts"), self.digests)
        second = self.backups / "second"
        pin2 = recovery.create(self.data, self.assets, self.artifacts, second, self.digests)
        (second / "assets" / "cv-vince-liem.typ").write_bytes(b"changed")
        with self.assertRaises(ValueError):
            recovery.verify_archive(second, pin2, self.digests)

    def test_known_secret_in_retained_text_aborts_backup(self):
        with closing(sqlite3.connect(self.data / recovery.DB_NAME)) as db, db:
            db.execute("UPDATE opportunities SET notes=? WHERE id='role-1'", (CANARY.decode(),))
        archive = self.backups / "leaky"
        with self.assertRaisesRegex(ValueError, "known deployment secret"):
            recovery.create(self.data, self.assets, self.artifacts, archive, self.digests, [CANARY])
        self.assertFalse(archive.exists())

    def test_rehashed_archive_with_in_flight_delivery_is_not_restorable(self):
        archive = self.backups / "in-flight"
        recovery.create(self.data, self.assets, self.artifacts, archive, self.digests)
        db_path = archive / recovery.DB_NAME
        with closing(sqlite3.connect(db_path)) as db, db:
            db.execute("UPDATE delivery_items SET state='sending' WHERE id='delivery-1'")
        manifest_path = archive / "manifest.json"
        manifest = json.loads(manifest_path.read_bytes())
        manifest["files"][recovery.DB_NAME] = recovery.file_digest(db_path)
        manifest_path.write_bytes(json.dumps(manifest, sort_keys=True, separators=(",", ":")).encode() + b"\n")
        with self.assertRaisesRegex(ValueError, "runnable recruitment"):
            recovery.verify_archive(archive, recovery.file_digest(manifest_path), self.digests)

    def test_rehashed_delivery_material_mismatch_is_rejected(self):
        for column, value in (("mime_bytes", b"changed saved message"),
                              ("attachment_sha256", "0" * 64),
                              ("pack_content_sha256", "0" * 64)):
            with self.subTest(column=column):
                archive = self.backups / column
                recovery.create(self.data, self.assets, self.artifacts, archive, self.digests)
                db_path = archive / recovery.DB_NAME
                with closing(sqlite3.connect(db_path)) as db, db:
                    db.execute(f"UPDATE delivery_items SET {column}=? WHERE id='delivery-1'", (value,))
                manifest_path = archive / "manifest.json"
                manifest = json.loads(manifest_path.read_bytes())
                manifest["files"][recovery.DB_NAME] = recovery.file_digest(db_path)
                manifest_path.write_bytes(json.dumps(manifest, sort_keys=True, separators=(",", ":")).encode() + b"\n")
                with self.assertRaisesRegex(ValueError, "saved delivery MIME or referenced pack"):
                    recovery.verify_archive(archive, recovery.file_digest(manifest_path), self.digests)

    def test_packaged_script_works_without_source_checkout(self):
        bundle = self.root / "bundle"
        subprocess.run([str(recovery.ROOT / "ops/i26/build-recovery.sh"), str(bundle)], check=True, capture_output=True)
        spec = importlib.util.spec_from_file_location("bundled_recovery", bundle / "recovery.py")
        packaged = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(packaged)
        self.assertEqual(packaged.schema_spec()["schemaSha256"], recovery.schema_spec()["schemaSha256"])
        archive = self.backups / "packaged"
        pin = packaged.create(self.data, self.assets, self.artifacts, archive, self.digests, [CANARY])
        self.assertEqual(len(pin), 64)
        packaged.verify_archive(archive, pin, self.digests)
        subprocess.run([sys.executable, str(bundle / "recovery.py"), "--help"], check=True, capture_output=True)

    def test_capture_manifest_round_trip_keeps_evidence_retrievable(self):
        archive = self.backups / "captures"
        pin = recovery.create(self.data, self.assets, self.artifacts, archive, self.digests)
        manifest = recovery.verify_archive(archive, pin, self.digests)
        self.assertEqual(manifest["captureCount"], 3)
        self.assertEqual(manifest["receiptCount"], 2)
        self.assertEqual(manifest["runEventCount"], 3)
        self.assertEqual(manifest["runCheckpointCount"], 1)
        self.assertEqual(manifest["captures"]["cap-1"],
                         {"sha256": self.sha_fetch, "ref": self.ref_fetch, "size": len(self.blob_fetch)})
        self.assertEqual(manifest["captures"]["cap-3"]["sha256"], self.sha_fetch)
        self.assertEqual(manifest["captures"]["cap-3"]["ref"], self.ref_fetch)
        self.assertEqual(manifest["receipts"], ["rcpt-1", "rcpt-2"])
        record = json.loads((archive / "executor-identity.json").read_bytes())
        self.assertEqual({entry["backend"] for entry in record["executors"]}, {"fixture-backend", "fixture-browser"})
        restored_data = private_dir(self.root, "cap-data")
        restored_assets = private_dir(self.root, "cap-assets")
        restored_artifacts = private_dir(self.root, "cap-artifacts")
        recovery.restore(archive, pin, restored_data, restored_assets, restored_artifacts, self.digests)
        self.assertEqual((restored_artifacts / self.ref_fetch).read_bytes(), self.blob_fetch)
        self.assertEqual((restored_artifacts / self.ref_search).read_bytes(), self.blob_search)
        with closing(sqlite3.connect(restored_data / recovery.DB_NAME)) as db:
            sha, ref, size = db.execute(
                "SELECT content_sha256,artifact_ref,byte_length FROM source_captures WHERE id='cap-1'").fetchone()
            body = (restored_artifacts / ref).read_bytes()
            self.assertEqual(recovery.digest(body), sha)
            self.assertEqual(len(body), size)
            receipt_ref, capture_id = db.execute(
                "SELECT receipt_ref,capture_id FROM research_observations WHERE id='obs-1'").fetchone()
            rec = json.loads((restored_artifacts / "receipts" / (receipt_ref + ".json")).read_bytes())
            self.assertEqual(rec["captureId"], self.sha_fetch)
            self.assertEqual(capture_id, "cap-1")
            fingerprint = db.execute("SELECT fingerprint FROM research_requests WHERE id='req-fresh'").fetchone()[0]
            self.assertEqual(rec["fingerprint"], fingerprint)

    def test_tampered_blob_detected_on_backup_and_verify(self):
        (self.artifacts / self.ref_fetch).write_bytes(b"tampered bytes")
        with self.assertRaisesRegex(ValueError, "capture bytes disagree"):
            recovery.create(self.data, self.assets, self.artifacts, self.backups / "tampered-live", self.digests)
        (self.artifacts / self.ref_fetch).write_bytes(self.blob_fetch)
        archive = self.backups / "tampered-archive"
        pin = recovery.create(self.data, self.assets, self.artifacts, archive, self.digests)
        (archive / "captures" / self.ref_fetch).write_bytes(b"tampered bytes")
        with self.assertRaises(ValueError):
            recovery.verify_archive(archive, pin, self.digests)
        manifest_path = archive / "manifest.json"
        manifest = json.loads(manifest_path.read_bytes())
        manifest["files"]["captures/" + self.ref_fetch] = recovery.file_digest(archive / "captures" / self.ref_fetch)
        manifest_path.write_bytes(json.dumps(manifest, sort_keys=True, separators=(",", ":")).encode() + b"\n")
        with self.assertRaisesRegex(ValueError, "capture bytes disagree"):
            recovery.verify_archive(archive, recovery.file_digest(manifest_path), self.digests)

    def test_forged_receipt_binding_and_missing_blob_rejected(self):
        path = self.artifacts / "receipts" / "rcpt-1.json"
        rec = json.loads(path.read_bytes())
        rec["fingerprint"] = "0" * 64
        path.write_bytes(json.dumps(rec).encode())
        with self.assertRaisesRegex(ValueError, "receipt fingerprint disagrees"):
            recovery.create(self.data, self.assets, self.artifacts, self.backups / "forged", self.digests)
        path.write_bytes(json.dumps({"id": "rcpt-1", "operation": "fetch", "fingerprint": self.fp_fresh,
                                     "status": "ok", "captureId": self.sha_fetch, "attempts": 1},
                                    separators=(",", ":")).encode())
        (self.artifacts / self.ref_search).unlink()
        with self.assertRaisesRegex(ValueError, "private artifact"):
            recovery.create(self.data, self.assets, self.artifacts, self.backups / "missing", self.digests)

    def test_restore_invalidates_claims_and_retains_uncertain_without_dispatch(self):
        archive = self.backups / "scrub"
        pin = recovery.create(self.data, self.assets, self.artifacts, archive, self.digests)
        restored_data = private_dir(self.root, "scrub-data")
        restored_assets = private_dir(self.root, "scrub-assets")
        restored_artifacts = private_dir(self.root, "scrub-artifacts")
        recovery.restore(archive, pin, restored_data, restored_assets, restored_artifacts, self.digests)
        with closing(sqlite3.connect(restored_data / recovery.DB_NAME)) as db:
            self.assertEqual(db.execute("SELECT state,lease_owner,lease_generation,lease_until FROM research_requests WHERE id='req-claimed'").fetchone(),
                             ("uncertain", None, None, None))
            self.assertEqual(db.execute("SELECT state FROM research_requests WHERE id='req-fresh'").fetchone()[0], "fresh")
            self.assertEqual(db.execute("SELECT state FROM research_requests WHERE id='req-uncertain'").fetchone()[0], "uncertain")
            active, evidence, unresolved, next_work, generation = db.execute(
                "SELECT active_claims_json,evidence_ids_json,unresolved_attempts_json,next_work_json,generation FROM run_checkpoints WHERE round_id='round-1'").fetchone()
            self.assertEqual(active, "[]")
            self.assertEqual(evidence, '["cap-1","cap-2"]')
            self.assertEqual(unresolved, '[{"attempt":"attempt-1","reason":"uncertain"}]')
            self.assertEqual(next_work, '[{"op":"fetch"}]')
            self.assertEqual(generation, 1)
            self.assertEqual(db.execute("SELECT generation FROM rounds WHERE id='round-1'").fetchone()[0], 2)
            self.assertEqual(db.execute("SELECT outcome FROM research_observations WHERE id='obs-2'").fetchone()[0], "uncertain")
            self.assertEqual(db.execute("SELECT outcome,is_late,capture_id FROM research_observations WHERE id='obs-3'").fetchone(),
                             ("late", 1, "cap-1"))
            self.assertEqual(db.execute("SELECT COUNT(*) FROM run_events").fetchone()[0], 3)
            self.assertEqual(db.execute("SELECT COUNT(*) FROM research_requests WHERE state='claimed'").fetchone()[0], 0)
            self.assertEqual(db.execute("SELECT COUNT(*) FROM run_checkpoints WHERE active_claims_json<>'[]'").fetchone()[0], 0)
            self.assertEqual(db.execute("SELECT COUNT(*) FROM jobs WHERE state IN ('queued','running')").fetchone()[0], 0)
            self.assertEqual(db.execute("SELECT COUNT(*) FROM rounds WHERE state IN ('queued','running','awaiting_input','stopping','paused')").fetchone()[0], 0)
            self.assertEqual(db.execute("SELECT COUNT(*) FROM delivery_items WHERE state='sending'").fetchone()[0], 0)

    def test_orphan_blobs_and_transient_files_are_skipped(self):
        orphan = self.artifacts / "blobs" / "ff" / ("ff" * 32)
        orphan.parent.mkdir(parents=True, mode=0o700)
        orphan.write_bytes(b"orphaned bytes")
        orphan.chmod(0o600)
        stray = self.artifacts / "receipts" / "stray.json"
        stray.write_bytes(b"{}")
        stray.chmod(0o600)
        cache = self.artifacts / "cache"
        cache.mkdir(mode=0o700)
        (cache / "scratch.tmp").write_bytes(b"transient")
        archive = self.backups / "orphans"
        pin = recovery.create(self.data, self.assets, self.artifacts, archive, self.digests)
        manifest = recovery.verify_archive(archive, pin, self.digests)
        self.assertEqual(manifest["skippedOrphanBlobs"], 1)
        self.assertEqual(manifest["skippedOrphanReceipts"], 1)
        self.assertEqual(manifest["ignoredArtifactEntries"], 1)
        self.assertEqual(manifest["captureCount"], 3)
        restored_artifacts = private_dir(self.root, "orphan-artifacts")
        recovery.restore(archive, pin, private_dir(self.root, "orphan-data"), private_dir(self.root, "orphan-assets"),
                         restored_artifacts, self.digests)
        self.assertFalse((restored_artifacts / "blobs" / "ff").exists())
        self.assertFalse((restored_artifacts / "receipts" / "stray.json").exists())
        self.assertFalse((restored_artifacts / "cache").exists())
        self.assertEqual((restored_artifacts / self.ref_fetch).read_bytes(), self.blob_fetch)

    def test_restore_refuses_nonempty_artifact_dir(self):
        archive = self.backups / "refuse"
        pin = recovery.create(self.data, self.assets, self.artifacts, archive, self.digests)
        target = private_dir(self.root, "dirty-artifacts")
        (target / "junk").write_bytes(b"existing")
        with self.assertRaisesRegex(ValueError, "artifact directory must be empty"):
            recovery.restore(archive, pin, private_dir(self.root, "refuse-data"), private_dir(self.root, "refuse-assets"),
                             target, self.digests)

    def test_storage_exhaustion_and_blob_bound_surface_explicitly(self):
        archive = self.backups / "room"
        pin = recovery.create(self.data, self.assets, self.artifacts, archive, self.digests)
        with mock.patch.object(recovery.shutil, "disk_usage", return_value=mock.Mock(free=1)):
            with self.assertRaisesRegex(ValueError, "storage exhausted"):
                recovery.create(self.data, self.assets, self.artifacts, self.backups / "noroom", self.digests)
            with self.assertRaisesRegex(ValueError, "storage exhausted"):
                recovery.restore(archive, pin, private_dir(self.root, "x-data"), private_dir(self.root, "x-assets"),
                                 private_dir(self.root, "x-artifacts"), self.digests)
        with mock.patch.object(recovery, "MAX_BLOB_BYTES", 8):
            with self.assertRaisesRegex(ValueError, "exceeds the backup size bound"):
                recovery.create(self.data, self.assets, self.artifacts, self.backups / "oversize", self.digests)

    def test_backup_without_research_captures_round_trips(self):
        bare = private_dir(self.root, "bare-data")
        db_path = bare / recovery.DB_NAME
        with closing(sqlite3.connect(db_path)) as db, db:
            db.execute(recovery.MIGRATION_TABLE_SQL)
            for version, name, sha, sql in recovery.source_migrations():
                db.executescript(sql)
                db.execute("INSERT INTO schema_migrations VALUES (?,?,?,?)", (version, name, sha, NOW))
        db_path.chmod(0o600)
        empty_artifacts = private_dir(self.root, "bare-artifacts")
        archive = self.backups / "bare"
        pin = recovery.create(bare, self.assets, empty_artifacts, archive, self.digests)
        manifest = recovery.verify_archive(archive, pin, self.digests)
        self.assertEqual(manifest["captureCount"], 0)
        self.assertEqual(manifest["receiptCount"], 0)
        self.assertEqual(manifest["captures"], {})
        self.assertEqual(manifest["receipts"], [])
        restored_artifacts = private_dir(self.root, "bare-restored-artifacts")
        recovery.restore(archive, pin, private_dir(self.root, "bare-restored-data"),
                         private_dir(self.root, "bare-restored-assets"), restored_artifacts, self.digests)
        self.assertTrue((restored_artifacts / "blobs").is_dir())
        self.assertTrue((restored_artifacts / "receipts").is_dir())
        self.assertEqual(json.loads((restored_artifacts / "executor-identity.json").read_bytes()), {"executors": []})


if __name__ == "__main__":
    unittest.main()
