<!--
  Usage → AI pause: the 50% guard's state and the owner's two controls.
  Resume records the owner's one go-ahead (until usage next reaches the cutoff
  after a weekly reset) and wakes the agents the guard paused; Pause now stops every agent. Wake catches agents a
  pause left paused after the owner resumed. Teammates see the state only.
  GET /pages/usage/ai, POST /pages/usage/ai/{resume,pause,wake}.
-->
<script lang="ts">
	import { onMount } from 'svelte';
	import { founderosFetch } from '$lib/founderos/api';
	import { Badge, Pressable, SlabCard } from '$lib/founderos/kit';

	type Guard = {
		cutoff: number;
		paused: boolean;
		reason?: string;
		canControl: boolean;
		usedPercent?: number;
		resetsAt?: string;
		goAhead?: boolean;
		stillPaused?: string[];
	};
	type Resumed = { resumed: string[]; kept: string[]; failed: string[]; warning?: string };

	let g = $state<Guard | null>(null);
	let failure = $state<string | null>(null);
	let note = $state<string | null>(null);
	let busy = $state(false);

	const when = (iso?: string) =>
		iso
			? new Date(iso).toLocaleString('en-GB', { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })
			: 'the weekly reset';
	const names = (xs: string[]) => xs.join(', ');

	// The paused-agent list reads the agent board; ask for it on open and
	// after an action, not on every poll.
	async function load(agents = false) {
		try {
			const next = await founderosFetch<Guard>(agents ? '/pages/usage/ai?agents=1' : '/pages/usage/ai');
			// Light polls omit the agent list: keep the last one while still running.
			if (!agents && g && !next.paused && next.goAhead) next.stillPaused = g.stillPaused;
			g = next;
			failure = null;
		} catch (e) {
			failure = e instanceof Error ? e.message : String(e);
		}
	}

	async function act(fn: () => Promise<void>) {
		busy = true;
		note = null;
		try {
			await fn();
		} catch (e) {
			note = e instanceof Error ? e.message : String(e);
		} finally {
			busy = false;
			await load(true);
		}
	}

	const resume = () =>
		act(async () => {
			const r = await founderosFetch<Resumed>('/pages/usage/ai/resume', { method: 'POST' });
			const parts = [`Resumed until usage next reaches ${g?.cutoff ?? 50}% after a weekly reset.`];
			if (r.resumed.length) parts.push(`Woke ${names(r.resumed)}.`);
			if (r.kept.length) parts.push(`${names(r.kept)} ${r.kept.length === 1 ? 'stays' : 'stay'} paused (paused before the cutoff).`);
			if (r.failed.length) parts.push(`Could not wake ${names(r.failed)}.`);
			if (r.warning) parts.push(r.warning);
			note = parts.join(' ');
		});
	const wake = () =>
		act(async () => {
			const r = await founderosFetch<Resumed>('/pages/usage/ai/wake', { method: 'POST' });
			const parts = [r.resumed.length ? `Woke ${names(r.resumed)}.` : 'No agents to wake.'];
			if (r.failed.length) parts.push(`Could not wake ${names(r.failed)}.`);
			note = parts.join(' ');
		});
	const pause = () =>
		act(async () => {
			const r = await founderosFetch<{ paused: string[]; failed?: string[]; warning?: string }>('/pages/usage/ai/pause', { method: 'POST' });
			const parts = [`Paused${r.paused.length ? ' ' + names(r.paused) : ''}.`];
			if (r.failed?.length) parts.push(`Could not stop ${names(r.failed)} yet; the guard keeps retrying.`);
			if (r.warning) parts.push(r.warning);
			note = parts.join(' ');
		});

	onMount(() => {
		void load(true);
		const t = setInterval(() => void load(), 30_000);
		return () => clearInterval(t);
	});
</script>

<SlabCard i={0} title="AI pause" sub={`all AI work stops at ${g?.cutoff ?? 50}% of the Codex weekly limit`}>
	{#if failure && !g}
		<p class="font-mono text-[11px] opacity-70">Could not read the AI pause: {failure}</p>
	{:else if g}
		<div class="flex flex-wrap items-center gap-3 font-mono text-[11px]">
			<Badge tone={g.paused ? 'warn' : 'ok'}>{g.paused ? 'paused' : 'running'}</Badge>
			{#if g.usedPercent !== undefined}
				<span>{Math.round(g.usedPercent)}% of the weekly limit used{g.resetsAt ? ` · resets ${when(g.resetsAt)}` : ''}</span>
			{/if}
			{#if !g.paused && g.goAhead}
				<span class="opacity-70">on your go-ahead until usage next reaches {g.cutoff}% after a weekly reset</span>
			{/if}
		</div>
		{#if !g.paused && g.stillPaused?.length}
			<p class="mt-2 font-mono text-[11px] opacity-70">Still paused: {names(g.stillPaused)}</p>
		{/if}
		{#if g.paused && g.reason}
			<p class="mt-2 font-mono text-[11px] opacity-70">{g.reason}</p>
		{/if}
		{#if g.canControl}
			<div class="mt-3 flex gap-2">
				{#if g.paused}
					<Pressable tone="primary" disabled={busy} onclick={resume}>Resume</Pressable>
				{:else}
					{#if g.stillPaused?.length}
						<Pressable tone="primary" disabled={busy} onclick={wake}>Wake {g.stillPaused.length} {g.stillPaused.length === 1 ? 'agent' : 'agents'}</Pressable>
					{/if}
					<Pressable tone="secondary" disabled={busy} onclick={pause}>Pause now</Pressable>
				{/if}
			</div>
		{:else if g.paused}
			<p class="mt-2 font-mono text-[11px] opacity-70">Only the owner can resume AI work.</p>
		{/if}
		{#if note}
			<p class="mt-2 font-mono text-[11px]" role="status">{note}</p>
		{/if}
	{/if}
</SlabCard>
