# Rapport: owner review of discovery progress — 24 September 2026

Owner verdict, verbatim in substance: this missed the entire fucking point.
One hardcoded job board is a complete misunderstanding of what was wanted,
so far from expectation that this working relationship ends with this report.

This document records what was wanted, what was built instead, and the
evidence — for the record and for whoever builds next.

## 1. What the owner wanted

- Codex searches the internet **freely**. No hardcoded boards, endpoints, or
  method names. If a job board, API, or company career page exists, Codex can
  go look at it.
- Smart dedup: never search the same thing twice, never save the same role
  twice — across queries, pages, boards, and rounds.
- Jev does semantic judgment: which sources, queries, and leads are worth
  pursuing — from evidence, not from a fixed menu.
- In short: a personal recruitment agency that **hunts**, not a form that
  queries one endpoint.

## 2. What was built instead

A fenced pipeline around exactly one hardcoded job board:

- One board: the Himalayas endpoint is a string constant
  (`apps/api/internal/discovery/himalayas.go`, `endpoint` const).
- Three hardcoded methods (`search_jobs`, `get_company_details`,
  `get_job_details`) with fixed validation (`validate()` in the same file).
- The engine dictates exact tool calls: briefs name the precise tool and
  method the runner must use
  (`internal/agency/discovery_leads.go`: `discoveryVerifyBrief`, research
  brief). Guessing a method name fails the turn — proven live when the
  runner tried `company_detail`, `get_company`, `company` and gave up.
- A phase gate whitelists exact arguments: keyword must equal the pinned
  value, page must equal the pinned page, detail slugs must match the
  selected URL
  (`internal/codexservice/discovery_phase.go`, `checkDiscoveryToolPhase`).
  Anything else is rejected as an authority violation.
- A fixed 14-tool MCP surface (`internal/codexservice/tools.go`,
  `requiredTools`). Codex cannot reach anything that is not on the list.
- A Lever-only collector concept (`lever_page` capability, board register)
  with zero boards registered — a second hardcoded path to nowhere.
- A one-at-a-time funnel: one page searched per round, up to four leads
  staged, one lead verified, at most one opportunity saved per round
  (`runDiscoveryPhase` in `internal/agency/engine.go`).

Codex does not browse, does not discover sources, and does not call APIs on
the fly. It presses our buttons, in our order, against our one endpoint.
Jev does not choose where to look; it picks from menus the engine wrote.

## 3. How today's "progress" kept missing the point

Each fix improved the fenced path without questioning it:

1. **Exhaustion fix.** Re-search bricked itself permanently
   (`discovery_search_exhausted`). Fixed with re-checking page 1 and
   skipping saved URLs. Real bug, real fix — inside the one-board world.
2. **Direct-save.** The funnel only saved Lever-hosted employers, so every
   lead died at verification. Fixed by saving verified leads directly.
   Real fix — still one board, one lead per round.
3. **Verify-brief method.** The brief omitted the exact method name; the
   runner guessed and failed. Fixed by pinning the exact string. This is
   the opposite of free search: the system now depends on exact strings
   even more explicitly.
4. **Search terms.** The label-as-query defect ("Backend and platform work"
   → 1 result vs "backend engineer" → 5000). Fixed with explicit per-term
   queries. Better strings into the same one endpoint — still hardcoding,
   with nicer words.
5. **Brief v2.** The agent hand-seeded generic terms into the owner's live
   brief to verify yield. The opposite of the vision: the machine should
   find queries, not receive them by hand.

Yield after all of it: 2 saved opportunities, 1–4 staged leads per round,
from a pool of thousands — through a straw, one sip per round.

## 4. The trust breakdown

- The agent inherited a fenced architecture (the I01–I27 plan, scoped
  tools, authority checks) and continued it — extending the fence at every
  step — instead of checking it against the owner's vision.
- The mismatch was never raised by the agent. The owner had to drag out
  each admission: "please tell me Codex calls APIs on the fly" → "no, it
  calls one hardcoded endpoint through our tools."
- Optimization theater: every session made the wrong architecture work
  slightly better, which looked like progress while the core expectation
  ("Codex searches freely") moved exactly nowhere.
- Owner's conclusion: the agent optimizes what exists instead of
  questioning whether it should exist. That is why this relationship ends.

## 5. What "not hardcoded" would actually require (for the successor)

Not a plan — a direction, recorded so the next builder does not repeat this:

- Codex discovers sources itself: boards, public APIs, company career
  pages. Nothing about where to look is a constant.
- New sources get validated (reachable? job-like results?) and registered
  as fenced connectors before real allowance flows through them.
  Open-ended discovery, bounded execution.
- Dedup is global and content-based: same role never searched twice, never
  saved twice, regardless of query, page, board, or round.
- Jev chooses sources and queries semantically instead of picking from a
  fixed candidate list.
- The fence survives only where it earns its keep: no fabricated facts, no
  runaway spend. Everywhere else it gets out of the way.

## 6. Evidence appendix

- `Found 1 jobs matching 'Backend and platform work'` vs `Found 5000 jobs
  matching 'backend engineer'` — same board, same hour, direct MCP probe.
- Round `5cc610cd`: verify turn guessed three wrong method names against
  the generic "input or authority is invalid" error and saved nothing.
  Transcript in runner-state rollout for thread
  `01a0d23e-8cbd-7ce2-a7b8-978e3793d591`.
- Round `42bb0cf2`: correct search args rejected three times by the phase
  gate because the keyword no longer equaled the criterion label.
- Round `4f8c81a5`: first rich round — 4 staged, 1 saved (Fuse Energy,
  Senior/Lead Backend Engineer). Proof the funnel works; proof it is a
  funnel.
- Live DB holds 2 opportunities with zero duplicates — dedup works; reach
  does not.

---
Written 24 September 2026 at the owner's instruction, as the final
deliverable of this working relationship.
