# Client AI SDR pods: design and build plan

Owner direction (8 Nov 2026): build everything thefoundersgroup.net promises, for real
clients, visible in the client portal. Do not remove or downgrade any website claim.
Codex reviews the design and each milestone. This file is the source of truth; the
build log lives in `~/Vaults/TFG Brain/03 SOPs/Client pods build log.md`.

## What the website promises (the acceptance list)

Every item must work for a client and be visible in their portal.

| # | Promise (site wording) | Built in |
|---|---|---|
| P1 | AI SDR finds the right buyers: ICP match, signals, decision-maker | M1, M3 |
| P2 | Writes like your best rep, in the client's voice, no templates | M1 |
| P3 | Client approves messaging and target lists before anything goes live | M1, M2 |
| P4 | Follows up day and night, email + LinkedIn | M1, M7 |
| P5 | Inbound enquiries answered in minutes, evenings and weekends | M3 |
| P6 | Qualifies budget, timing and who decides, in conversation | M3 |
| P7 | Books into reps' calendars, with reminders | M4 |
| P8 | Logs everything in the client's CRM (HubSpot, Pipedrive, Salesforce) | M5 |
| P9 | Call brief before every meeting | M4 |
| P10 | Weekly pipeline review, rep coaching on real calls, "what to work on" | M6 |
| P11 | Results against the month-one baseline, weekly report in the portal | M2, M6 |
| P12 | Protected, warmed-up separate sending domains | M1 (per-pod identity), M7 (domains) |
| P13 | A person takes over when a buyer asks for one, fully briefed | M3 |
| P14 | Works with HubSpot, Pipedrive, Salesforce, Gmail, Outlook, LinkedIn, Slack, Meet | M4, M5, M7 |
| P15 | Live in two weeks; Diagnose month; you own everything (hand-off) | M1, M8 |
| P16 | Client portal: setup, approvals, call briefs, results, deliverables, sessions | M2 |

## Shape

- **A pod is a workspace.** Each client gets a TFG OS workspace (`client-<slug>`) and a
  `founderos_client_pods` row: portal client id, package, reps, status
  (`setup` → `dry_run` → `live` → `paused` → `handed_over`), sending identity
  (email account, from name, sending domain), approval policy, caps, timezone and sending
  window, CRM and calendar bindings, baseline and targets.
- **Knowledge per pod.** ICP, voice, product and pricing, objections, target accounts and
  exclusions are pod documents. Agents receive only that pod's context. The owner's HQ mail,
  calendar and Slack never become a pod source (existing rule in `api/sales_sources.go`).
- **Outbox per pod.** The `founderos` hard-coding goes; every send path (approve, follow-ups,
  campaign auto-send, owner mail) loops over pods with each pod's policy, caps, window,
  suppression list and sending account. Suppression and "never email twice" are per pod plus
  a global do-not-contact list.
- **Approval policy per pod.** `client_approves_each`, `client_approves_sequences` (a step
  approved once is used for every recipient) or `auto`. Client approvals happen in the portal.
- **Dry run.** A pod in `dry_run` runs end to end, but every outbound message is delivered
  to the TFG operator's inbox with a banner and nothing reaches prospects. This is the test
  run before go-live.
- **Kill switch.** Pausing a pod (OS or portal) stops all sends within one pass.

## Portal (Supabase project `tfg-backend`)

The portal stays on Supabase. TFG OS syncs to it with the service-role key (stored only in
`~/.founderos/.env`):

- push: pod setup state, approval requests (sequences, messages, target lists), activity
  feed, meetings and call briefs, weekly results;
- pull: client approvals and edits, pause/resume, connected-system requests.

New portal views mirror the website tabs: **Setup, Approvals, Call briefs, Results**, plus an
Activity feed and Settings (pause, notification email). Tables and RLS are versioned in the
site repo under `supabase/migrations`.

## Milestones

Each milestone: failing tests first, implementation, full gates (go test, vitest,
svelte-check 0 errors), Codex review, fixes, deploy, build-log entry.

- **M1 Pod foundation:** schema, pod CRUD and admin view in /os/clients, per-pod outbox and
  sending identity, approval policies, dry-run, kill switch, per-pod daily routines in
  Paperclip (research, outreach) fed only pod context.
- **M2 Portal:** version the existing Supabase schema; add pod tables + RLS; sync worker;
  portal Setup / Approvals / Activity / Results views.
- **M3 Inbound + conversations:** per-pod inbound endpoint (form webhook and forwarded inbox),
  research-then-reply within minutes, qualification (budget, timing, decision-maker), human
  takeover on request with a brief to the rep.
- **M4 Booking + call briefs:** booking links per rep (Cal.com / Google Calendar), reminders,
  booked → stop sequences → call brief to rep email + portal.
- **M5 CRM sync:** adapter interface; HubSpot first (existing integration), then Pipedrive,
  Salesforce; contacts, companies, deals, activity logging.
- **M6 Coaching + reporting:** call recordings (Fathom/Plaud) → coaching notes; weekly
  pipeline review; weekly results vs baseline in the portal.
- **M7 Channels needing owner purchases:** warmed separate domains per pod, LinkedIn via a
  provider, contact enrichment (Apollo plan), Outlook/Meet.
- **M8 Hand-off + end-to-end test:** export bundle (playbook, sequences, reports, CRM
  config), onboarding checklist, full dry-run of a fictional client from signature to
  hand-off, then Codex review of the whole system.

## Codex design review (8 Nov 2026), adopted

Guarantees added to M1 (full review in the build log):

- **Identity:** `connectors/email` has four installation-wide inboxes and silently falls back to
  inbox 1 for a missing or unknown `AccountID`. Pods need an account registry; dispatch must
  reject missing, unknown or foreign accounts and pin the approved identity revision.
- **Isolation:** every request checks workspace membership (a slug is not authorization);
  bookings match attendees within the pod only; agent packets carry no HQ handover or data;
  connectors resolve pod-bound credentials, never installation ones, for pod work.
- **Exactly-once:** reserve per pod + recipient + step before sending; caps checked inside the
  reservation; `uncertain` outcomes are never retried automatically.
- **Dry run:** an explicit approved sink, all original recipients stripped, simulated receipts
  kept apart from live history and caps.
- **Pause:** re-checked immediately before dispatch.
- **Approvals:** immutable revisions for messages, sequences and target lists; any edit
  invalidates approval; client live sending waits for portal approval evidence (M2).
- **Routines:** per-pod Paperclip routines provisioned idempotently, bindings stored in the OS,
  scoped packets, pause blocks dispatch of already-running tasks.

### M1 checklist (TDD, in order)

1. [x] Pod store: lifecycle, sender, docs, activity (commit 884d186)
2. [x] Pod API with workspace-membership authorization; two pods can't touch each other
3. [x] Per-pod mailbox (pod:<slug> keys), unknown/missing refused
4. [x] Pod outbox API, dispatcher, importers routed by board project
5. [x] One send gate for manual, campaign, follow-up and owner sends: policy, pause, window, caps
6. [x] Reservations per pod+recipient+step; uncertain never retried
7. [x] Revision-bound approvals for messages, sequences, target lists
8. [x] Suppression, per-pod replies and opt-outs, bookings scoped per workspace
9. [x] Dry run via approved sink with separate simulated receipts
10. [x] Agent packets per pod (no HQ handover); routines provisioned per pod, idempotently
11. [ ] /os/clients admin view; full gates; Codex review
