# Remaining work

The owner rejected the fixed-board discovery architecture on 24 September 2026 and instructed its complete removal. Read [the owner report](discovery-rapport-2026-09-24.md) before proposing further discovery work.

The autonomous-recruitment replacement is implemented through T28 of the [ordered task checklist](/Users/vince/Projects/find-income/dashboard/docs/autonomous-recruitment-tasks.md) and packaged as candidate `rc1-20260924-df808dd-a71533b0` ([manifest](/Users/vince/Projects/find-income/implementation-notes/autonomous-recruitment/T28-candidate/manifest.md)). All repository gates are green on the candidate revision ([verification note](/Users/vince/Projects/find-income/implementation-notes/autonomous-recruitment/T28-candidate/note.md)).

## What is proven vs what is not

Implemented and fixture-verified: composed backend/API integration (T23), runtime failure and concurrency behavior (T24), current-format backup/restore on composed data (T25), identity/evidence outcomes against the frozen corpus (T26), and browser/product acceptance with scripted turns (T27). Fixture evidence only: corpus captures are verbatim frozen bytes over fixture HTTP, Jev/model turns are scripted or doubles, and spend is zero. Live behavior — real sources, real model turns, real captures, real host confinement — is NOT proven.

## Deferred live acceptance (owner decision 2026-09-24)

T29 (host verification), T30 (live canary), and T31 (live audit) are DEFERRED pending host selection. Their checklist boxes stay unchecked; nothing below relabels them as done.

- **T29** needs: the selected host (`infra` vs `linux`, see [host readiness](host-readiness.md)), the prepared private fresh-install path with actual account/tool access from [runner operations](/Users/vince/Projects/find-income/dashboard/ops/i12/README.md), and one operator owning host/config changes.
- **T30** needs: T29 green, the unchanged owner brief, an explicit finite canary allowance, and one operator commissioning the single run.
- **T31** needs: completed T30 outcomes to audit.

## Known limits of the candidate

Uncommitted tree (identity rests on the manifest's content digest, not a git ref); nil D `Supersedes` resolver (fresh assessments fine, reassessment chains unlinked); no live run yet; Linux amd64 binaries cross-compiled and never executed here. Full list with digests: [manifest](/Users/vince/Projects/find-income/implementation-notes/autonomous-recruitment/T28-candidate/manifest.md).

## Recovery

Current-format backup/verify/restore steps are in [private recovery](/Users/vince/Projects/find-income/dashboard/ops/i26/README.md) (format `jobseek-current-backup-v2`, proven round-trip on composed T23 data in T25). No live-host restore has been performed; that proof belongs to the deferred host work.

## Owner usefulness

Pending live feedback: no canary has run, so usefulness of saved outcomes against real vacancies is unmeasured. T31's audit rubric ([live-audit-rubric](/Users/vince/Projects/find-income/implementation-notes/autonomous-recruitment/T04-corpus/live-audit-rubric.md)) is prepared and waiting on T30.

Follow [AGENTS.md](/Users/vince/Projects/find-income/AGENTS.md), including direct prototype changes without backward compatibility, Codex-owned row entry and three fully reworded Jev consultations for difficult design decisions. The implemented research path does not authorize employer contact, delivery or live provider spending; those need the explicit deferred-work decisions above.
