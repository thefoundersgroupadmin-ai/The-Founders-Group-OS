# TFG OS task-agent structure

Every registered built-in worker has a separate, durable Paperclip task-agent counterpart. The original CEO, Prospect Research, Sales Operations and Sales Enablement and Delivery agents remain. There are currently 35 worker roles plus these four agents: 39 task agents in total.

A registered worker executes its implemented connector or local read. Its task agent owns a work order, applies the shared workflow methods, uses authorised source records, produces documents and records completion or an explicit blocker. A configured role is not proof that an external service or an unsupported action works. Action-level limitations for legacy key-only and local rendering workers appear in the coverage panel.

## Operation

Use **Conductor** or **Skills → Give the OS a command**. The planner selects the relevant task roles and workflows. Selecting a task role automatically refreshes its associated worker first. A worker-only plan is appropriate for an explicit diagnostic, not as a substitute for doing a task. Automatic worker refreshes are recorded as evidence-only: their gaps remain visible but do not fail a completed internal task. Explicitly requested worker operations remain required.

Commands share one sequential execution lane on this Mac. Each task receives the approved workspace profile, scoped source packet, linked worker evidence, bounded previous-step outputs, the complete workflow catalogue, the attached owner handover and a verified weekly usage check. Communication roles can receive actual inbox previews when the owner operates in HQ or Personal; sales workspaces retain prospect filtering. Worker evidence redacts credential fields and is persisted in the command's recorded steps. Legacy worker-run history retains its existing summary format.

Task results must include a saved `report` document. The command retrieves those actual documents, attaches them to later steps, publishes its result to Conductor and notifies the owner. Research and draft email documents use the existing Pipeline and pending Outbox import. No external send, publication, payment or credit spending is implied by provisioning a role. Unsupported operations and missing accounts must be reported explicitly.

**Agents** and **Skills** show live role coverage, total task agents, available agents, running agents and connection requirements. The home dashboard's old “agents live” count is now labelled “Worker profiles active”: it is a database configuration count, not AI execution or API health.

## Provisioning and identity

Owner-only `POST /api/founderos/pages/agents/fleet/provision` creates only missing roles. The durable identity is `metadata.tfgWorkerId`, not the display name. Repeating setup preserves existing roles and paused states. Parents are provisioned before children using the existing OS hierarchy. New agents use the existing OpenAI subscription runtime, managed instructions, on-demand wake, one run per agent, and no permission to create further agents or skills. Secrets, host commands and privilege flags are not copied from a template.

The `conductor` worker's task counterpart is named **Conductor Operations** to keep it distinct from the existing cockpit responder. This avoids unintentionally moving the standing Conductor conversation onto a new task-only agent.

An existing working OpenAI subscription binding is required. A separate model API key is not required for every role. Provisioning does not bypass company approval rules or change those rules.

## Credentials and current operation limits

Use **OS Connections** to enter credentials. Never paste secret values into chat, reports or this document. A stored key is not an API verification result.

- Calling: Twilio Account SID and Auth Token, a funded account and caller number; complete Dialer setup. Public webhook delivery must also be verified.
- The existing dialer phone lookup uses Apollo credits. Lusha contact email/phone enrichment now has a task-tool adapter; it requires LUSHA_API_KEY and API credits, with live provider validation still outstanding. A subscription alone does not prove API access.
- Call analysis: Fathom API key and/or Plaud refresh token for the recording sources actually used.
- Financial reads: Stripe secret key, and the appropriate PayKit, PayPal, Square or Whop credentials only if those processors are used. These are alternatives, not a requirement to subscribe to every service.
- Social and newsletter sources: Zernio token, Arcads authorization, Beehiiv key and publication, ManyChat key for the applicable lanes. Check each connector's verified state and operation coverage before claiming publishing or generation.
- Optional lead capture/attribution: Typeform key, Trakyo credentials. These do not block the existing OS pipeline or research tasks.

FlexPay is excluded at the owner's request. Legacy PayKit aliases still only check key presence; use verified finance reads for evidence.

The task tool gateway supplies a closed 30-tool catalogue. Role profiles restrict discovery; server checks independently enforce role, live run, owner command and workspace. Shared API credentials stay in the OS. Mutations and paid enrichment/generation create exact action requests in **OS Approvals**; approval executes once and returns a durable receipt to the task. Reusing a request UUID cannot repeat an operation. Uncertain outcomes need provider inspection, never an automatic retry.

Lusha uses LUSHA_API_KEY for bounded v3 contact email/phone reveals. Higgsfield uses HIGGSFIELD_API_KEY for model submission and task-owned job polling/cancellation. Remotion needs REMOTION_RENDER_URL and REMOTION_RENDER_TOKEN for a compatible render service implementing POST /render, GET /jobs/:id and POST /jobs/:id/cancel; this adapter does not provision a renderer. These pathways still require credentials and live provider validation. Lusha results are returned to the task; automatic phone/email merging into the pipeline is not implemented by the restricted pipeline-update tool.

Run scripts/install-agent-tools.py internally to install the gateway, refresh its catalogue, bind role profiles and install the connection for those agents. Profiles alone do not install connections. The public gateway path requires its strong service credential and validates actual task/run context; the main OS stays behind Cloudflare Access.

An explicit owner resume after the weekly cutoff may be recorded internally in ~/.founderos/ai-resume.json (authorizedAt, expiresAt, reason). It lasts at most four hours, cannot override a newer pause, and leaves the original pause record intact. The helper and backend both enforce expiration. No background agent is resumed merely by recording that authorization.

All AI execution remains subject to the persistent owner-controlled 50% weekly usage cutoff. The product checks are cooperative safeguards, not a verified account-wide kill switch.

## Failure handling and validation

The command runner checks native task execution as well as issue status. A failed native run cannot remain indefinitely presented as running merely because its issue says `in_progress`. Provider usage-limit failures stop the command before subsequent task launches. Known exhausted short-term windows also block new AI work; no provider failover or automatic replay of failed work is added. Task creation uses a stable per-command-step idempotency key to avoid duplicates after an uncertain HTTP response.

Official usage discovery includes both legacy company Codex homes and current `runtime/paperclip-runner/durable-sessions/*/codex-home/sessions`. Gauge timestamps are retained separately from later token activity, so an older gauge is not made to look newly observed.

Validation: all 35 task-role runtime/account probes passed. Department-level real work and sequential handoffs are tested separately. An initial oversized commissioning command hit the short-term account limit; it was stopped for repair and its unfinished test tasks cancelled as superseded. Saved documents and native failures remain in history. Do not interpret that initial batch as 35 successful completed work orders.
