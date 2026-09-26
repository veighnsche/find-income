# Product vision (authoritative — owner's words, 25 Sep 2026)

This document overrides all agent-derived flows, contracts, and roadmaps where they conflict.
The app NEVER sends anything to employers. Step 7 handoff is the terminal state.

1. find out my search goal where I fill in what i want and what I don't want in the shape of regular deterministic forms
2. use muse spark 1.3 contributor CLI to find jobs freely without hardcoding any job boards and shit, Jev classifies them under 5 categories recommend, might recommend, might not recoomend, not recommended or unknown (unknwon is rarely used)
3. Selection of the jobs I find interesting
4. use muse spark 1.3 contributor to do a deeper dive of the vacancy and how to apply and generate questions of things the vacancy wants to know
5. JEV prefills questions, and I can edit or add answers as well
6. use muse spark 1.3 standrd to make the CV, motivation, email or form values
7. hand it off to me. and tell me which website to go to etc... You save that into a list of jobs you have the artifacts for

## What this kills

- Automated employer contact: send review/authorization, SMTP delivery, attempts, outcomes, reconcile-before-retry.
- Session ceremony around the CLIs: supervisors, transports, lane verification, MCP session servers. The contributor/standard CLIs are invoked directly.
- Later lifecycle (replies, interviews, offers) until the owner asks for it.

## What this keeps

- Goals forms (wants/don't-wants, deterministic), free CLI search + 5-category Jev
  classification, selection, per-vacancy deep dive + question generation, Jev prefill
  with owner edit, standard-drafted CV/motivation/email/form values, exact-material
  review, handoff instructions, per-job artifact list.
