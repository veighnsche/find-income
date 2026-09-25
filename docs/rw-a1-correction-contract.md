# RW-A1 verified correction client contract

Correction is profile-targeted owner input through rounds. There is no parallel owner CRUD (`PUT /preferences` is deliberately unsupported).

## Calls

- Submit: `POST /api/v1/rounds/process-input` with `{requestKey, targetKind:"profile", targetId:"current", expectedRevision:<saved version>, text}` (client: `processInput`).
  - `201` commissioned (new round), `200` replay (same key + byte-identical input refs), `400` invalid target/text, `409` stale revision or changed input under a reused key, `503` runner not ready. No other success shape exists.
- Track: `GET /api/v1/rounds/{id}` until terminal (`state`, `profileVersion`, `originalProfileVersion`, `report`, `unresolved`, `outcome`).
- Readback: `GET /api/v1/preferences`; a correction counts as saved only when the returned `version` exceeds the submitted `expectedRevision` AND equals the round `profileVersion`. Accepted (201) is never presented as effective.

## Saved-version facts (server truth, `store/preferences.go`)

- Versioned `Preferences`: `preferredLocation`, `allowRemote`/`allowHybrid` (booleans — there is NO `workPattern` enum; correction text must map onsite/hybrid/remote onto the two booleans), `targetHoursHundredths` (100–16800), `minMonthlyBaseCents` (≥0), `salaryCurrency` (code), `timezone` (valid zone), `roleCriteria[]` (`{id,label,description,kind,mode}`; ≤32; `kind` ∈ role/responsibility/technology, `mode` ∈ require/avoid/prefer).
- There are no `role_criteria_*`/`criteria_*` columns: desired-role specifics live in `roleCriteria[]`, and brief authoring emits one `BriefFact{Key: criterion.ID, Value: mode + label}` per entry.
- Paused-round replacement (`replacePaused`) is revision-checked and replacement-scoped; a stale `expectedRevision` conflicts instead of overwriting.
- Missing Jev classification/catalog before the first run is normal; the UI must not invent catalog states.

## Evidence

- `process_input_test.go`: profile apply + rebind + `input_applied` report, same-key replay, stale 409, paused replacement.
- `TestProcessInputHTTPReadbackConfirmsSavedCorrection` (new): HTTP submit → HTTP round poll → HTTP preferences readback (version bump + applied value) + invalid-target/kind 400s.
