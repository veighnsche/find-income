# Public ATS collector slice

The first supported connector reads configured Lever public posting sites through
`GET /v0/postings/{site}?mode=json&skip=N&limit=25`. Lever documents both the
global and EU hosts, pagination, `descriptionPlain`, `lists`, and `hostedUrl` in
its [Postings API reference](https://github.com/lever/postings-api).

The owner configures each board in `collector_boards` through the store API.
There is no built-in board list and fresh databases make no collector requests.
Each enabled board has a persisted cursor, interval, lease, and last safe error.
One run makes at most two page requests and advances the cursor only after all
postings in those pages enter the shared ingestion queue. A short final page
resets the cursor to zero. Re-reading a posting with identical response bytes
reuses its idempotent intake; changed response bytes make a new sourced intake.

`OriginalText` is the exact JSON object for one posting returned in the Lever
list response. It includes all fields supplied for that posting, including
plain descriptions and list HTML when present. It is **not** a capture of the
hosted HTML page, attachments, or employer facts that Lever did not expose.
The collector validates the posting's hosted URL against the configured site
and region before submitting it. Oversized, malformed, and unexpected payloads
fail the board run with a safe error code; no partial text is invented.

Pagination can shift if a board changes while a multi-run sweep is underway.
The next complete sweep begins at offset zero; source hashes and idempotency
avoid duplicate work. Collection enqueues intake only. The separate Codex
ingestion worker must process it before an opportunity is saved and Jev can
organise it.
