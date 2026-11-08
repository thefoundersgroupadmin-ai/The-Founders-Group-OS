<!-- /os/pods/[slug]: one client's AI SDR pod. Status moves, setup checklist,
     sending mailbox, documents (with the client's approval), the AI SDR on the
     task board, the pod's outbox (every send goes through the pod gate) and
     its activity. Data: /api/founderos/pods/:slug/... -->
<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { founderosFetch } from '$lib/founderos/api';
	import { Badge, PageHeader, Pressable, Rise, Slab } from '$lib/founderos/kit';
	import { APPROVAL_BADGE, DOC_LABELS, STATUS_BADGE, actionsFor, parseDomains, setupChecklist, waitingReason } from '$lib/founderos/pages/pods/pods';
	import type { Activity, AgentBinding, ClientApproval, Mailbox, Pod, PodDoc } from '$lib/founderos/pages/pods/types';
	import { STATUS_BADGE as OUTBOX_BADGE, recipientLine } from '$lib/founderos/pages/outbox/outbox';
	import type { OutboxBody, OutboxItem } from '$lib/founderos/pages/outbox/types';

	const slug = $derived(page.params.slug ?? '');
	const base = $derived(`/pods/${encodeURIComponent(slug)}`);

	let pod = $state<Pod | null>(null);
	let failure = $state<string | null>(null);
	let mailbox = $state<Mailbox | null>(null);
	let docs = $state<Record<string, PodDoc | undefined>>({});
	let drafts = $state<Record<string, string>>({});
	let outbox = $state<OutboxBody | null>(null);
	let activity = $state<Activity[]>([]);
	let agent = $state<AgentBinding | null>(null);
	let mb = $state({ host: '', user: '', pass: '', from: '', fromName: '', smtpHost: '', smtpPort: '' });
	let reason = $state('');
	let note = $state('');
	let busy = $state(false);
	let openDoc = $state('icp');
	let approvals = $state<ClientApproval[]>([]);
	let portalId = $state('');
	let list = $state({ title: '', domains: '' });
	let rules = $state<{ approvalPolicy: string; firstEmailDailyCap: number; followUpDailyCap: number; timezone: string; sendWindow: [number, number]; sendWeekends: boolean; dryRunInbox: string; bookingUrl: string; crmProvider: string; calendarProvider: string } | null>(null);

	const err = (e: unknown) => (e instanceof Error ? e.message : 'request failed');

	async function load() {
		try {
			pod = (await founderosFetch<{ pod: Pod }>(base)).pod;
			rules ??= {
				approvalPolicy: pod.approvalPolicy, firstEmailDailyCap: pod.firstEmailDailyCap, followUpDailyCap: pod.followUpDailyCap,
				timezone: pod.timezone, sendWindow: [...pod.sendWindow] as [number, number], sendWeekends: pod.sendWeekends,
				dryRunInbox: pod.dryRunInbox, bookingUrl: pod.bookingUrl, crmProvider: pod.crmProvider, calendarProvider: pod.calendarProvider
			};
		} catch (e) {
			failure = err(e);
			return;
		}
		founderosFetch<{ mailbox: Mailbox }>(`${base}/mailbox`).then((b) => (mailbox = b.mailbox)).catch(() => {});
		founderosFetch<OutboxBody>(`${base}/outbox`).then((b) => (outbox = b)).catch(() => {});
		founderosFetch<{ activity: Activity[] }>(`${base}/activity`).then((b) => (activity = b.activity)).catch(() => {});
		founderosFetch<{ agent: AgentBinding | null }>(`${base}/agent`).then((b) => (agent = b.agent)).catch(() => {});
		founderosFetch<{ approvals: ClientApproval[] }>(`${base}/approvals`).then((b) => (approvals = b.approvals)).catch(() => {});
		portalId ||= pod?.portalClientId ?? '';
		for (const key of Object.keys(DOC_LABELS)) {
			founderosFetch<{ doc: PodDoc }>(`${base}/docs/${key}`)
				.then((b) => {
					docs[key] = b.doc;
					drafts[key] ??= b.doc.body;
				})
				.catch(() => {
					docs[key] = undefined;
					drafts[key] ??= '';
				});
		}
	}
	onMount(load);

	async function act(fn: () => Promise<unknown>) {
		busy = true;
		note = '';
		try {
			await fn();
			await load();
		} catch (e) {
			note = err(e);
		} finally {
			busy = false;
		}
	}

	const setStatus = (to: string, needsReason?: boolean) =>
		act(() => founderosFetch(`${base}/status`, { method: 'POST', json: { status: to, reason: needsReason ? reason : '' } }));
	const saveMailbox = () =>
		act(async () => {
			await founderosFetch(`${base}/mailbox`, { method: 'PUT', json: mb });
			mb = { ...mb, pass: '' };
		});
	const linkPortal = () => act(() => founderosFetch(`${base}/portal`, { method: 'PUT', json: { portalClientId: portalId } }));
	const proposeList = () =>
		act(async () => {
			await founderosFetch(`${base}/target-lists`, { method: 'POST', json: { title: list.title, domains: parseDomains(list.domains) } });
			list = { title: '', domains: '' };
		});
	const saveRules = () => act(() => founderosFetch(`${base}/settings`, { method: 'PUT', json: rules }));
	const saveDoc = (key: string) => act(() => founderosFetch(`${base}/docs/${key}`, { method: 'PUT', json: { body: drafts[key] ?? '' } }));
	const recordApproval = (key: string) => {
		const by = prompt('Who approved it, and how? (e.g. "Priya Shah by email, 9 Nov")');
		if (by) act(() => founderosFetch(`${base}/docs/${key}/approve`, { method: 'POST', json: { by } }));
	};
	const provision = () =>
		act(async () => {
			agent = (await founderosFetch<{ agent: AgentBinding }>(`${base}/agent`, { method: 'POST' })).agent;
		});
	const decide = (it: OutboxItem, action: 'approve' | 'reject') =>
		act(() =>
			founderosFetch(`${base}/outbox/${encodeURIComponent(it.id)}/${action}`, {
				method: 'POST',
				json: action === 'approve' ? { subject: it.subject, body: it.body } : {}
			})
		);

	const checklist = $derived(
		pod
			? setupChecklist(
					pod,
					{ configured: !!mailbox?.configured },
					Object.fromEntries(Object.entries(docs).map(([k, d]) => [k, d && d.body ? { approved: !!d.approvedAt } : undefined])),
					agent
				)
			: []
	);
</script>

<div>
	<PageHeader eyebrow="client AI SDR" title={pod?.clientName ?? 'Client'}>
		{#snippet right()}
			{#if pod}<Badge tone={STATUS_BADGE[pod.status].tone}>{STATUS_BADGE[pod.status].label}</Badge>{/if}
		{/snippet}
	</PageHeader>
	{#if failure}
		<Slab><p class="font-mono text-[12px]" style="color: var(--bn-err)">This client pod could not be read: {failure}</p></Slab>
	{:else if !pod}
		<Slab><p class="bn-dim animate-pulse font-mono text-[12px]">loading…</p></Slab>
	{:else}
		<Rise i={1}>
			{#if note}<Slab><p class="font-mono text-[12px]" style="color: var(--bn-err)">{note}</p></Slab>{/if}

			<Slab>
				<p class="bn-dim mb-2 font-mono text-[10px] uppercase tracking-[0.15em]">Status</p>
				{#if pod.status === 'paused'}<p class="mb-2 text-[12px]">Paused: {pod.pausedReason}</p>{/if}
				{#if pod.missingToGoLive?.length}
					<p class="mb-2 text-[12px]" style="color: var(--bn-warn)">To go live it still needs: {pod.missingToGoLive.join(', ')}.</p>
				{/if}
				<div class="flex flex-wrap items-center gap-2">
					{#each actionsFor(pod) as a (a.to)}
						<Pressable tone={a.to === 'live' ? 'primary' : 'secondary'} disabled={busy || (a.needsReason && reason.trim() === '')} onclick={() => setStatus(a.to, a.needsReason)}>{a.label}</Pressable>
					{/each}
					{#if actionsFor(pod).some((a) => a.needsReason)}
						<input bind:value={reason} placeholder="Reason for pausing" aria-label="Reason for pausing" class="pc-input min-w-[200px] rounded-[6px] px-2.5 py-1.5 text-[12px]" />
					{/if}
				</div>
				<p class="bn-dim mt-2 font-mono text-[11px]">
					{pod.package} · {pod.reps} reps · approval: {pod.approvalPolicy.replaceAll('_', ' ')} · {pod.firstEmailDailyCap} first emails / {pod.followUpDailyCap} follow-ups a day ·
					{pod.sendWindow[0]}:00–{pod.sendWindow[1]}:00 {pod.timezone}{pod.sendWeekends ? ' incl. weekends' : ''}
				</p>
			</Slab>

			<Slab>
				<p class="bn-dim mb-2 font-mono text-[10px] uppercase tracking-[0.15em]">Setup</p>
				{#each checklist as item (item.key)}
					<div class="flex items-center justify-between gap-2 py-1">
						<span class="text-[12px]">{item.done ? '✓' : '○'} {item.label}</span>
						<span class="bn-dim font-mono text-[11px]">{item.detail}</span>
					</div>
				{/each}
			</Slab>

			{#if rules}
				<Slab>
					<p class="bn-dim mb-2 font-mono text-[10px] uppercase tracking-[0.15em]">Sending rules</p>
					<div class="grid grid-cols-1 gap-2 md:grid-cols-3">
						<label class="text-[11px]">Client approval
							<select bind:value={rules.approvalPolicy} class="pc-input mt-1 w-full rounded-[6px] px-2 py-1.5 text-[12px]">
								<option value="client_approves_each">Every email</option>
								<option value="client_approves_sequences">Approved sequences</option>
								<option value="auto">Automatic (no client approval)</option>
							</select>
						</label>
						<label class="text-[11px]">First emails a day<input type="number" min="0" max="500" bind:value={rules.firstEmailDailyCap} class="pc-input mt-1 w-full rounded-[6px] px-2 py-1.5 text-[12px]" /></label>
						<label class="text-[11px]">Follow-ups a day<input type="number" min="0" max="1000" bind:value={rules.followUpDailyCap} class="pc-input mt-1 w-full rounded-[6px] px-2 py-1.5 text-[12px]" /></label>
						<label class="text-[11px]">Timezone<input bind:value={rules.timezone} class="pc-input mt-1 w-full rounded-[6px] px-2 py-1.5 text-[12px]" /></label>
						<label class="text-[11px]">Sending from (hour)<input type="number" min="0" max="23" bind:value={rules.sendWindow[0]} class="pc-input mt-1 w-full rounded-[6px] px-2 py-1.5 text-[12px]" /></label>
						<label class="text-[11px]">Sending until (hour)<input type="number" min="1" max="24" bind:value={rules.sendWindow[1]} class="pc-input mt-1 w-full rounded-[6px] px-2 py-1.5 text-[12px]" /></label>
						<label class="text-[11px]">Test-run inbox<input bind:value={rules.dryRunInbox} placeholder="where test emails go" class="pc-input mt-1 w-full rounded-[6px] px-2 py-1.5 text-[12px]" /></label>
						<label class="text-[11px]">Booking link<input bind:value={rules.bookingUrl} placeholder="https://cal.com/…" class="pc-input mt-1 w-full rounded-[6px] px-2 py-1.5 text-[12px]" /></label>
						<label class="text-[11px]">CRM
							<select bind:value={rules.crmProvider} class="pc-input mt-1 w-full rounded-[6px] px-2 py-1.5 text-[12px]">
								<option value="">None yet</option><option value="hubspot">HubSpot</option><option value="pipedrive">Pipedrive</option><option value="salesforce">Salesforce</option>
							</select>
						</label>
						<label class="text-[11px]">Calendar
							<select bind:value={rules.calendarProvider} class="pc-input mt-1 w-full rounded-[6px] px-2 py-1.5 text-[12px]">
								<option value="">None yet</option><option value="calcom">Cal.com</option><option value="google">Google Calendar</option><option value="microsoft">Outlook</option>
							</select>
						</label>
						<label class="flex items-center gap-2 text-[11px]"><input type="checkbox" bind:checked={rules.sendWeekends} /> Send at weekends</label>
					</div>
					<div class="mt-2"><Pressable tone="secondary" disabled={busy} onclick={saveRules}>Save rules</Pressable></div>
				</Slab>
			{/if}

			<Slab>
				<p class="bn-dim mb-2 font-mono text-[10px] uppercase tracking-[0.15em]">Sending mailbox</p>
				{#if mailbox?.configured}
					<p class="mb-2 text-[12px]">{mailbox.fromName || pod.senderName} &lt;{mailbox.from || mailbox.user}&gt; via {mailbox.smtpHost || mailbox.host} · password {mailbox.pass}</p>
				{/if}
				<div class="grid grid-cols-1 gap-2 md:grid-cols-2">
					<input bind:value={mb.fromName} placeholder="Sender name (e.g. Sam | Brightvale)" aria-label="Sender name" class="pc-input rounded-[6px] px-2.5 py-1.5 text-[12px]" />
					<input bind:value={mb.from} placeholder="Send-as address (optional)" aria-label="Send-as address" class="pc-input rounded-[6px] px-2.5 py-1.5 text-[12px]" />
					<input bind:value={mb.host} placeholder="IMAP host (e.g. imap.gmail.com)" aria-label="IMAP host" class="pc-input rounded-[6px] px-2.5 py-1.5 text-[12px]" />
					<input bind:value={mb.user} placeholder="Mailbox login" aria-label="Mailbox login" class="pc-input rounded-[6px] px-2.5 py-1.5 text-[12px]" />
					<input bind:value={mb.pass} type="password" placeholder="App password" aria-label="App password" class="pc-input rounded-[6px] px-2.5 py-1.5 text-[12px]" />
					<input bind:value={mb.smtpHost} placeholder="SMTP host (optional)" aria-label="SMTP host" class="pc-input rounded-[6px] px-2.5 py-1.5 text-[12px]" />
				</div>
				<div class="mt-2">
					<Pressable tone="secondary" disabled={busy || !mb.host || !mb.user || !mb.pass} onclick={saveMailbox}>{mailbox?.configured ? 'Replace mailbox' : 'Connect mailbox'}</Pressable>
				</div>
			</Slab>

			<Slab>
				<p class="bn-dim mb-2 font-mono text-[10px] uppercase tracking-[0.15em]">Documents</p>
				<div class="mb-2 flex flex-wrap gap-1">
					{#each Object.entries(DOC_LABELS) as [key, d] (key)}
						<button class="bn-pill rounded-full border px-3 py-1 text-[11px]" class:is-on={openDoc === key} onclick={() => (openDoc = key)}>
							{docs[key]?.approvedAt ? '✓ ' : ''}{d.label}
						</button>
					{/each}
				</div>
				<p class="bn-dim mb-1 font-mono text-[11px]">{DOC_LABELS[openDoc].hint}{docs[openDoc] ? ` · v${docs[openDoc]?.version}` : ''}{docs[openDoc]?.approvedAt ? ' · approved' : docs[openDoc] ? ' · not approved' : ''}</p>
				<textarea bind:value={drafts[openDoc]} rows="8" aria-label={DOC_LABELS[openDoc].label} class="pc-input w-full resize-y rounded-[6px] px-2.5 py-2 text-[12px] leading-relaxed"></textarea>
				<div class="mt-2 flex flex-wrap gap-2">
					<Pressable tone="secondary" disabled={busy} onclick={() => saveDoc(openDoc)}>Save</Pressable>
					<Pressable tone="ghost" disabled={busy || !docs[openDoc] || !!docs[openDoc]?.approvedAt} onclick={() => recordApproval(openDoc)}>Record client approval</Pressable>
				</div>
			</Slab>

			<Slab>
				<p class="bn-dim mb-2 font-mono text-[10px] uppercase tracking-[0.15em]">Client portal</p>
				<div class="flex flex-wrap items-center gap-2">
					<input bind:value={portalId} placeholder="Portal client id (from the CRM)" aria-label="Portal client id" class="pc-input min-w-[320px] flex-1 rounded-[6px] px-2.5 py-1.5 font-mono text-[12px]" />
					<Pressable tone="secondary" disabled={busy} onclick={linkPortal}>{pod.portalClientId ? 'Update link' : 'Link portal'}</Pressable>
				</div>
				<p class="bn-dim mt-1 font-mono text-[11px]">{pod.portalClientId ? 'Linked: the client sees status, approvals and activity in their portal, synced every 2 minutes.' : 'Not linked yet.'}</p>
				<p class="bn-dim mt-3 mb-1 font-mono text-[10px] uppercase tracking-[0.15em]">Client approvals</p>
				{#each approvals as a (a.kind + a.ref)}
					<div class="flex flex-wrap items-center justify-between gap-2 py-0.5">
						<span class="text-[12px]">{a.title}</span>
						<Badge tone={APPROVAL_BADGE[a.status].tone}>{APPROVAL_BADGE[a.status].label}</Badge>
					</div>
				{:else}
					<p class="bn-dim font-mono text-[12px]">Nothing sent for approval yet.</p>
				{/each}
				<p class="bn-dim mt-3 mb-1 font-mono text-[10px] uppercase tracking-[0.15em]">Propose a target list</p>
				<input bind:value={list.title} placeholder="List name (e.g. Multi-site warehouses)" aria-label="Target list name" class="pc-input mb-2 w-full rounded-[6px] px-2.5 py-1.5 text-[12px]" />
				<textarea bind:value={list.domains} rows="4" placeholder="Company domains or websites, one per line" aria-label="Target list domains" class="pc-input w-full resize-y rounded-[6px] px-2.5 py-2 text-[12px]"></textarea>
				<div class="mt-2 flex items-center gap-2">
					<Pressable tone="secondary" disabled={busy || !list.title.trim() || parseDomains(list.domains).length === 0} onclick={proposeList}>Send to client for approval</Pressable>
					<span class="bn-dim font-mono text-[11px]">{parseDomains(list.domains).length} companies</span>
				</div>
			</Slab>

			<Slab>
				<p class="bn-dim mb-2 font-mono text-[10px] uppercase tracking-[0.15em]">AI SDR</p>
				<p class="mb-2 text-[12px]">
					{#if agent}Provisioned: its own agent, project and daily research (07:00) and outreach (08:00) routines, {agent.routinesActive ? 'running' : 'paused until the pod runs'}.{:else}Gives this client its own agent on the task board, briefed only with this client's documents.{/if}
				</p>
				<Pressable tone="secondary" disabled={busy} onclick={provision}>{agent ? 'Refresh AI SDR' : 'Provision AI SDR'}</Pressable>
			</Slab>

			<Slab>
				<p class="bn-dim mb-2 font-mono text-[10px] uppercase tracking-[0.15em]">Outbox {outbox ? `· ${outbox.pending.length} waiting` : ''}</p>
				{#if outbox}
					{#each outbox.pending as it (it.id)}
						<div class="border-b py-2" style="border-color: var(--bn-hairline)">
							<div class="flex flex-wrap items-center justify-between gap-2">
								<span class="text-[12px]">to {recipientLine(it)} · {it.subject}</span>
								<span class="flex gap-2">
									<Pressable tone="secondary" disabled={busy} onclick={() => decide(it, 'approve')}>Send now</Pressable>
									<Pressable tone="ghost" disabled={busy} onclick={() => decide(it, 'reject')}>Reject</Pressable>
								</span>
							</div>
							{#if it.error}<p class="bn-dim mt-1 font-mono text-[11px]">{waitingReason(it.error)}</p>{/if}
						</div>
					{:else}
						<p class="bn-dim font-mono text-[12px]">Nothing waiting.</p>
					{/each}
					{#each outbox.recent.slice(0, 15) as it (it.id)}
						<div class="flex flex-wrap items-center justify-between gap-2 py-1">
							<span class="truncate text-[12px]">{recipientLine(it)} · {it.subject}</span>
							<Badge tone={OUTBOX_BADGE[it.status].tone}>{OUTBOX_BADGE[it.status].label}</Badge>
						</div>
					{/each}
				{/if}
			</Slab>

			<Slab>
				<p class="bn-dim mb-2 font-mono text-[10px] uppercase tracking-[0.15em]">Activity</p>
				{#each activity as a (a.at + a.summary)}
					<div class="flex gap-3 py-0.5">
						<span class="bn-dim shrink-0 font-mono text-[11px]">{a.at.slice(0, 16).replace('T', ' ')}</span>
						<span class="text-[12px]">{a.summary}</span>
					</div>
				{:else}
					<p class="bn-dim font-mono text-[12px]">No activity yet.</p>
				{/each}
			</Slab>
		</Rise>
	{/if}
</div>
