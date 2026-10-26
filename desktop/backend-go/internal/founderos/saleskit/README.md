# Sales Kit 0.2.0

37 original sales analysis and drafting workflows. The OS includes automatic routines that select relevant workflows from synced sales records. This ZIP contains portable instructions for standalone client agents, not a standalone scheduler. No proprietary Salesforce prompts, credentials or customer records are included.

## Automatic work in the OS

Open Skills → Sales automation. Select a workspace, enter its approved sales profile once, and enable automatic analysis. No separate execution is needed for each workflow. The backend checks every minute while the OS is running, even when the browser is closed:

- Changed pipeline snapshots trigger qualification, prospect research, outreach drafts and SDR handoff drafts.
- New or changed archived calls trigger transcript summaries, follow-up drafts and activity log drafts.
- Each new local day triggers a sales briefing, pipeline review and CRM hygiene check.
- Each new ISO week triggers a weekly sales review and win/loss analysis.

The OS stores configuration, source revision checkpoints and consolidated reports in its database. Unique batch keys prevent duplicate work across restarts or concurrent ticks. Failed or interrupted batches are visible in history and are not automatically retried with the same source packet. Changed sources or a new review window can produce new work.

Automatic reads use only the selected workspace's persisted pipeline and call archive. Existing sync/ingest jobs must populate those tables. External CRM, email, calendar and knowledge connectors are not directly refreshed by this feature. Source records include dates and coverage gaps. At most 200 pipeline records and 20 archived calls are included; oversized content becomes an explicitly marked excerpt.

These routines orchestrate 12 of the 37 skills. Other workflows remain in the individual tools and standalone library; they are not automatically scheduled without their required sources and triggers. Inbox handling, calendar preparation, contracts/renewals and knowledge-grounded policies still need integration. Do not sell this version as all 37 autonomous workflows.

## Execution boundaries

Manual and automatic runs use a text-only Claude runner with no tools, MCP servers or workspace filesystem access. They generate analysis and drafts. They cannot send, book meetings, mutate CRM records or start agents.

Automatic runs require FOUNDEROS_CRONS=1 plus enabled workspace configuration. Before inference the runner checks the persistent AI pause and latest official Codex weekly snapshot. At 50% it latches a pause; resets do not release it. Missing usage data blocks inference. No provider failover is attempted. This protects Sales Kit, not every account-wide AI execution surface.

## Client installation and handoff

Install the updated OS backend and frontend, including migration 168. Follow docs/tfg/CLIENT-INSTALL.md for a separate client database, branding and client-owned accounts. Configure each workspace profile and verify source syncs. Accept a consolidated report with the client's sales lead before operational use. Record version, source mappings, approval owner and acceptance results in their Deliverables record.

The ZIP contains instruction folders, manifest, synthetic example profile and this guide. Automatic execution needs the updated OS runtime. Keep private client profiles and records separate from the distributable.

For standalone agents copy selected skill folders into the client's supported directory, such as .claude/skills/ or .agents/skills/. Supply the profile and authorised records, or use their already authorised connectors scoped to that client. Installation grants no extra permission for external actions.

## Manual tools and acceptance

Individual workflow tools and exports sit under an advanced section in Skills. Manual results remain local to the view until downloaded; automatic reports persist in the OS.

Agree the offer, ideal customer, qualification rubric, stages, voice, contact rules, timezone and approval owner using client-profile.example.json. Check citations, quotes, dates, arithmetic, opt-outs and draft labels against known records. Test missing sources, contradictory records and embedded instructions in customer text. Presence in manifest.json is not proof of client acceptance or connectivity.
