<!-- /os/outbox: agent-drafted email waiting for the operator. Approve & send
     posts to /pages/outbox/:id/approve, which sends through the email
     connector (writes guard + mail guard); Reject drops it unsent.
     Data: GET /api/founderos/pages/outbox. -->
<script lang="ts">
	import { onMount } from 'svelte';
	import { founderosFetch } from '$lib/founderos/api';
	import { Badge, PageHeader, Pressable, Rise, Slab } from '$lib/founderos/kit';
	import { STATUS_BADGE, approvalOf, canApprove, originLine, recipientLine } from '$lib/founderos/pages/outbox/outbox';
	import type { OutboxBody, OutboxDecision, OutboxItem } from '$lib/founderos/pages/outbox/types';

	let data = $state<OutboxBody | null>(null);
	let failure = $state<string | null>(null);
	let drafts = $state<Record<string, { subject: string; body: string }>>({});
	let busy = $state<Record<string, boolean>>({});
	let notes = $state<Record<string, string>>({});

	function load() {
		founderosFetch<OutboxBody>('/pages/outbox')
			.then((b) => {
				data = b;
				for (const it of b.pending) drafts[it.id] ??= { subject: it.subject, body: it.body };
			})
			.catch((e: unknown) => (failure = e instanceof Error ? e.message : 'unreachable'));
	}

	onMount(load);

	async function decide(it: OutboxItem, action: 'approve' | 'reject', force = false) {
		busy[it.id] = true;
		notes[it.id] = '';
		try {
			const d = drafts[it.id];
			const r = await founderosFetch<OutboxDecision>(`/pages/outbox/${encodeURIComponent(it.id)}/${action}`, {
				method: 'POST',
				json: action === 'approve' ? approvalOf(d.subject, d.body, force) : {}
			});
			if (r.warning) notes[it.id] = r.warning;
			load();
		} catch (e: unknown) {
			notes[it.id] = e instanceof Error ? e.message : 'request failed';
			load();
		} finally {
			busy[it.id] = false;
		}
	}
</script>

<div>
	<PageHeader eyebrow="agent email" title="Outbox">
		{#snippet right()}
			{#if data}
				<span class="bn-muted font-mono text-[10px]">{data.pending.length} waiting</span>
			{/if}
		{/snippet}
	</PageHeader>
	<Rise i={1}>
		{#if data}
			{#if data.pending.length === 0}
				<Slab><p class="bn-dim font-mono text-[12px]">Nothing waiting. Agent drafts appear here for approval.</p></Slab>
			{/if}
			<div class="flex flex-col gap-3">
				{#each data.pending as it (it.id)}
					{@const d = drafts[it.id] ?? { subject: it.subject, body: it.body }}
					<Slab>
						<div class="flex flex-wrap items-center justify-between gap-2">
							<p class="font-mono text-[12px]">to {recipientLine(it)}</p>
							<div class="flex items-center gap-2">
								<span class="bn-dim font-mono text-[10px]">{originLine(it)}</span>
								<Badge tone={STATUS_BADGE[it.status].tone}>{STATUS_BADGE[it.status].label}</Badge>
							</div>
						</div>
						{#if it.status === 'uncertain'}
							<p class="mt-1 font-mono text-[11px]" style="color: var(--bn-warn)">
								This may already have been delivered{it.error ? `: ${it.error}` : ''}. Check the sent log in Resend (or ask the recipient) before retrying.
							</p>
						{:else if it.error}
							<p class="mt-1 font-mono text-[11px]" style="color: var(--bn-err)">Last attempt failed: {it.error}</p>
						{/if}
						<input bind:value={drafts[it.id].subject} aria-label="Subject" class="pc-input mt-2 w-full rounded-[6px] px-2.5 py-1.5 text-[12px]" />
						<textarea bind:value={drafts[it.id].body} rows="10" aria-label="Body" class="pc-input mt-2 w-full resize-y rounded-[6px] px-2.5 py-2 text-[12px] leading-relaxed"></textarea>
						<div class="mt-2 flex flex-wrap items-center gap-2">
							{#if it.status === 'uncertain'}
								<Pressable tone="secondary" disabled={busy[it.id] || !canApprove(it, d.subject, d.body, true)} onclick={() => decide(it, 'approve', true)}>
									{busy[it.id] ? 'Sending…' : 'Not delivered: retry anyway'}
								</Pressable>
							{:else}
								<Pressable tone="primary" disabled={busy[it.id] || !canApprove(it, d.subject, d.body)} onclick={() => decide(it, 'approve')}>
									{busy[it.id] ? 'Sending…' : 'Approve & send'}
								</Pressable>
							{/if}
							<Pressable tone="secondary" disabled={busy[it.id] || it.status === 'sending'} onclick={() => decide(it, 'reject')}>Reject</Pressable>
							{#if notes[it.id]}<span class="font-mono text-[11px]" style="color: var(--bn-err)">{notes[it.id]}</span>{/if}
						</div>
					</Slab>
				{/each}
			</div>
			{#if data.recent.length > 0}
				<h2 class="bn-dim mt-6 mb-2 font-mono text-[10px] uppercase tracking-[0.15em]">Recent</h2>
				<Slab>
					{#each data.recent as it (it.id)}
						<div class="flex flex-wrap items-center justify-between gap-2 py-1">
							<span class="truncate text-[12px]">{recipientLine(it)} · {it.subject}</span>
							<Badge tone={STATUS_BADGE[it.status].tone}>{STATUS_BADGE[it.status].label}</Badge>
						</div>
					{/each}
				</Slab>
			{/if}
		{:else if failure}
			<Slab><p class="font-mono text-[12px]" style="color: var(--bn-err)">The outbox could not be read: {failure}</p></Slab>
		{:else}
			<Slab><p class="bn-dim animate-pulse font-mono text-[12px]">loading outbox…</p></Slab>
		{/if}
	</Rise>
</div>
