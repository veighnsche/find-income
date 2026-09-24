# Current-format private recovery

This package backs up the **current** Jobseek SQLite schema and the four exact approved career source files used to prepare application packs. It does not export the project tree, source checkout, runner Codex home, service env, SSH/TLS keys or browser credentials. Pack manifests, immutable Typst source and PDFs already live in `application_packs` BLOBs; the separate files at `/var/lib/jobseek/assets` are the pinned source inputs for future pack preparation. The resulting directory is private career data and still belongs on an encrypted, access-controlled backup volume.

`recovery.py` uses Python's SQLite online backup API to obtain a consistent snapshot, even when the app uses WAL. It sanitizes the private scratch copy, then uses `VACUUM INTO` to build a new SQLite file with deleted/free-page content purged. It deletes administrator password hashes, browser sessions, agent credentials and round tool capabilities. It replaces historical job-attempt lease tokens, clears active job leases, fails all queued/running jobs and rounds, marks unfinished remote/Jev work and in-flight delivery submissions uncertain, and drops the transient qualification refresh queue. Opportunity, evidence, relationship, interview brief/debrief, offer comparison, delivery review/MIME, Jev response, round dispatch/history and immutable pack rows remain available for review. No job or round is dispatched by this script.

The output contains `jobseek.sqlite`, `assets/` with exactly `cv-vince-liem.typ`, `cv-vince-liem.md`, `github-evidence-review.md` and `portfolio-case-studies.md`, and `manifest.json`. The bundle carries current migration names/digests and a SQLite schema-object signature, independent of documentation commits. Backup and restore reject a different schema until this operational slice is reviewed against that new current format. Each pack's Go content hash, role revision, source body/digest and PDF header are checked. Saved delivery MIME and referenced PDF/pack digests must also agree. The manifest lists SHA-256 for every output file; keep its own SHA-256 separately so a corrupted or replaced manifest is detectable. `PRAGMA integrity_check` and `PRAGMA foreign_key_check` must pass on both sides.

## Build and install the script bundle

On a trusted build machine with this dashboard checkout, create an empty private bundle outside the checkout:

```sh
umask 077
dashboard/ops/i26/build-recovery.sh /private/tmp/jobseek-i26-bundle
cd /private/tmp/jobseek-i26-bundle && shasum -a 256 -c SHA256SUMS
```

Transfer the bundle through the authenticated private deployment channel to `/opt/jobseek/recovery` on the **app host**, owned by root with directory mode `0700` and its files mode `0600` (script `0700`). On Linux verify `(cd /opt/jobseek/recovery && sha256sum -c SHA256SUMS)` and record the manifest hash in the private deployment record. The target host needs Python 3 with SQLite support and sufficient free space for a temporary raw snapshot plus the compact export. Do not install this bundle on the runner VM or expose it through Caddy.

## Create and verify a backup

Run as root on the app host. Prepare an encrypted private parent directory such as `/var/backups/jobseek` with mode `0700`; choose a new output name. The script reads the deployed API env, runner SSH private key and TLS private key **only to scan the final output for those known values**; it never copies or prints them. It also checks the current sensitive-column inventory and fails if a new credential-bearing schema column has not been reviewed. The API env must contain the current `TYPESAFE_API_KEY` and `JOBSEEK_CODEX_BRIDGE_TOKEN`.

```sh
umask 077
install -d -m 0700 /var/backups/jobseek
python3 /opt/jobseek/recovery/recovery.py backup \
  --data-dir /var/lib/jobseek/data \
  --assets-root /var/lib/jobseek/assets \
  --out /var/backups/jobseek/2026-09-23-current \
  --api-env /etc/jobseek/api.env \
  --ssh-key /etc/jobseek/ssh/runner_key \
  --tls-key /etc/jobseek/tls/key.pem
```

The only stdout is the 64-character manifest SHA-256. Record it **outside** the backup directory in the private backup catalog. Verify with:

```sh
python3 /opt/jobseek/recovery/recovery.py verify \
  --archive /var/backups/jobseek/2026-09-23-current \
  --manifest-sha256 RECORDED_MANIFEST_SHA256
```

The backup source may be running because the online backup API snapshots SQLite consistently. Career source files must remain immutable and match their four pinned digests. A known deployment secret found anywhere in the final DB, manifest or asset bytes aborts and removes the output directory. Credential-like text pasted into an ordinary source/owner note cannot be identified without knowing its value; review such material before saving it, and protect the encrypted backup as private personal data.

## Restore on a fresh private app installation

Install the same current API schema and I12 app artifacts/configuration on the replacement app host, but **do not start the API**. Provide new service secrets, SSH identity, TLS material and private endpoint through I12 outside the archive. The I12 installer creates `/var/lib/jobseek/data` empty and places the four approved assets in `/var/lib/jobseek/assets`; the recovery script accepts that exact pinned set or an empty private asset directory. Stop `jobseek-api.service` on an existing host. Restore refuses any existing SQLite file/WAL/shm, other data-dir contents, an active API service, changed schema, damaged hashes, failed SQLite checks or a different asset set. It does not overwrite an existing installation.

```sh
systemctl stop jobseek-api.service
python3 /opt/jobseek/recovery/recovery.py restore \
  --archive /var/backups/jobseek/2026-09-23-current \
  --manifest-sha256 RECORDED_MANIFEST_SHA256 \
  --data-dir /var/lib/jobseek/data \
  --assets-root /var/lib/jobseek/assets
```

The restored SQLite file is mode `0600` for `jobseek-api`; new asset files are root-owned and API-group-readable, outside the public web root. The script starts no service or provider call. Set `JOBSEEK_CODEX_ISOLATION_VERIFIED=false` during recovery, create a **new** dashboard administrator credential with `jobseek-api setup-admin`, start the private services, and verify login, restored records/packs and idle row counts. Browser sessions, agent tokens, lease tokens and round capabilities are gone; issue new agent credentials only if needed. Codex OAuth state and subscription access live on the separate runner and are never in this backup. Use I12's supported owner device-code flow to reconnect on a newly isolated runner. Saved remote thread/turn IDs and uncertainty remain evidence, never permission to auto-retry.

This package is current-format only. The synthetic test below is not a production backup, host restore, owner login or proof that the final I27 schema has been covered:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s dashboard/ops/i26 -p 'test_*.py' -v
```

Primary SQLite references: [online backup API](https://sqlite.org/backup.html), [VACUUM INTO and deleted-content purge](https://sqlite.org/lang_vacuum.html), [integrity and foreign-key checks](https://sqlite.org/pragma.html).
