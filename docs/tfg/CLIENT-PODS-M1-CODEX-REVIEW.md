**M1 is not safe for live client outreach yet.** Reviewed `884d186^..187ead3`. The main blockers are a send-gate bypass, foreign sender bindings, missing step-level deduplication, and approval/dispatch races.

Paths below are relative to `desktop/backend-go/internal/founderos/` unless prefixed `scripts/`.

1. **Critical — Comms bypasses every pod safeguard.**  
   `api/page_comms.go:666`; `connectors/email/email.go:540`  
   `/pages/comms/reply` accepts an arbitrary `account`, including `pod:another-client`, then calls SMTP with `RecipientApproved: true`. It checks neither pod membership nor client approval, pause, suppression, caps, or window. With writes enabled, this sends unapproved client mail.  
   **Fix:** refuse pod accounts on this endpoint; route pod messages through an authenticated, workspace-scoped outbox gate. Enforce that boundary below the HTTP handlers too.

2. **Critical — A pod can bind another client’s mailbox or HQ’s identity.**  
   `pages/pods/store.go:197`; `api/page_pods.go:174`; `pages/outbox/podgate.go:132`  
   Any member allowed through `pod()` can change the sender. `SetSender` accepts arbitrary `AccountID`; the gate only compares the draft’s account against that stored value. Binding A to `pod:client-b` or `inbox-1` therefore passes. SMTP uses the configured mailbox’s From, regardless of the pod’s displayed sender.  
   **Fix:** restrict sender administration, require the exact pod-owned account, verify its actual From, and bind approvals to an identity revision.

3. **High — “Exactly once” is only enforced per outbox row, not recipient + step.**  
   `pages/outbox/podgate.go:146`; `scripts/sync-paperclip-outbox.py:199`  
   After a successful send, another row for the same recipient and sequence step is allowed. A new task or changed subject creates another ID; the gate treats it as a follow-up without checking step identity, minimum delay, or maximum follow-ups. Repeated approved sequence content can consequently send again on successive passes.  
   **Fix:** persist a unique reservation for pod + normalized recipient + sequence revision + step; enforce cadence and terminal sequence state there.

4. **High — Pause, suppression and approval withdrawal race with dispatch.**  
   `pages/outbox/podgate.go:219`; `pages/pods/store.go:309`  
   The gate commits and releases its advisory lock before SMTP. Status changes do not share that lock. A pause, opt-out, or approval withdrawal after the checks can complete before the message is handed over, yet dispatch still uses the stale decision.  
   **Fix:** coordinate those mutations with dispatch and revalidate current policy, approval revisions and identity immediately before transport handoff.

5. **High — Template matching permits unapproved substantive claims.**  
   `pages/outbox/podapprovals.go:37`  
   Placeholders accept arbitrary 1–80-character text excluding only newline and `. ! ?`. For example, `{first_name}` can contain `Alex; we guarantee £100000 revenue`. Punctuation restrictions do not constrain meaning; repeated placeholders need not even agree.  
   **Fix:** render templates server-side from typed, validated contact fields. Require explicit approval for any freeform personalization.

6. **High — Pipeline routing fails open into HQ.**  
   `scripts/sync-paperclip-pipeline.py:176`  
   A failed pod-binding query becomes `pods = {}`. Client tasks then use the default pipeline, and their other documents become eligible for HQ address extraction. A transient database/query failure can therefore import client research into TFG’s workspace.  
   **Fix:** abort on binding-query failure; quarantine unknown projects rather than treating them as HQ.

7. **High — Moving a Paperclip task can cross-contaminate existing drafts.**  
   `scripts/sync-paperclip-outbox.py:230`  
   Outbox IDs omit workspace. On conflict, the importer updates wording without checking or changing workspace. Move a previously imported pending task from A to B: B’s wording updates the row still owned by A. Under automatic policy, A can send B’s content.  
   **Fix:** pin immutable task ownership; reject routing changes for existing imports. Include workspace in keys and scope every conflict update.

8. **High — Reply handling both misses stops and blocks intended responses.**  
   `scripts/sync-prospect-replies.py:40,296`; `pages/outbox/podgate.go:159`  
   `watch_keys` retains one contact per company domain, so a reply from another employee marks the wrong contact engaged and may leave that employee’s follow-ups running. Conversely, once a contact is engaged, the gate rejects *every* subsequent email after an earlier send—including the requested, client-approved conversational response.  
   **Fix:** match exact senders first, persist conversation events, and distinguish sequence follow-ups from approved replies.

9. **Medium — Uncertain deliveries disappear from cap usage.**  
   `pages/outbox/podgate.go:197`  
   Counts include `sending` and `sent`, but exclude `uncertain`, although those messages may have delivered. Repeated uncertain outcomes across recipients allow actual deliveries beyond the cap.  
   **Fix:** count durable reservations, including uncertain outcomes, until explicitly reconciled.

10. **Medium — Failed schedule creation is never repaired.**  
    `pages/pods/agents.go:208`  
    The routine ID is saved before adding its trigger. If trigger creation fails, reprovisioning sees an existing routine and only updates its fields; no schedule is added. It can subsequently report routines active while they never run.  
    **Fix:** reconcile trigger existence on every provision; serialize provisioning per pod.

**Before onboarding the first real client**

- Fix the above and complete M1’s `/os/clients` administration. There is currently no API to configure the dry-run sink, policy, caps or sending window.
- Implement authenticated portal approvals and immutable approval evidence (P3/M2). The current target-list miss also returns before creating an approval request.
- Prove P1/P2/P4 end to end: outreach routines receive instructions but no explicit researched-prospect, send-history or suppression packet. Agent instructions alone do not establish reliable personalized sequences.
- Require a completed, approved dry run before live status; readiness currently checks sender email and approved ICP/voice, not successful simulation or mailbox readiness.
- Finish later promises before claiming them operational: portal/results, inbound qualification and takeover, client calendar bindings/reminders/briefs, CRM sync, coaching, protected warmed domains, LinkedIn and export/handoff. Booking scoping protects HQ versus pods; it does not connect client calendars.

**Validation:** 33 Python sync tests passed. Go tests could not start because the read-only sandbox denied temporary build-directory creation. No edits, sends, restarts, or secret reads performed.