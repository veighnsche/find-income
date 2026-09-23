# Public ATS collector slice

The first supported connector reads configured Lever public posting sites through
`GET /v0/postings/{site}?mode=json&skip=N&limit=25`. Lever documents both the
global and EU hosts, pagination, `descriptionPlain`, `lists`, and `hostedUrl` in
its [Postings API reference](https://github.com/lever/postings-api).

Fresh databases seed five enabled boards verified from official company careers
pages on 23 September 2026: Finom, Wypoon Technologies, Protolabs, Samba TV,
and Yuno. Their exact site/region and verification URLs are in
`internal/store/default-collector-boards.json`. The owner can disable them or
add more boards through the store API. Each enabled board has a persisted
cursor, interval, lease, and last safe error.
One run makes at most two page requests and advances the cursor only after all
valid postings in those pages enter the shared ingestion queue. A partial sweep
continues after one minute; a short final page resets the cursor to zero and
uses the configured refresh interval. Re-reading a posting with identical response bytes
reuses its idempotent intake; changed response bytes make a new sourced intake.

`OriginalText` is the exact JSON object for one posting returned in the Lever
list response. It includes all fields supplied for that posting, including
plain descriptions and list HTML when present. It is **not** a capture of the
hosted HTML page, attachments, or employer facts that Lever did not expose.
The collector validates the posting's hosted URL against the configured site
and region before submitting it. A malformed individual posting is skipped
with a safe board warning so later valid postings continue. A malformed page,
oversized response, or network error leaves the cursor in place for retry; no
partial text is invented.

Pagination can shift if a board changes while a multi-run sweep is underway.
The next complete sweep begins at offset zero; source hashes and idempotency
avoid duplicate work. Collection enqueues intake only. The separate Codex
ingestion worker must process it before an opportunity is saved and Jev can
organise it.
