<!-- The auto-repair doctor: what it checks every five minutes, what it fixed, and the few things only the owner can do. -->
<script lang="ts">
	import { Chip, SlabCard } from '$lib/founderos/kit';
	import { relativeTime } from './radar';
	import { repairHeadline, repairRows, statusLabel, type RepairState } from './repair';

	let { state }: { state: RepairState | null | undefined } = $props();
	const rows = $derived(repairRows(state));
	const head = $derived(repairHeadline(state));
	const actions = $derived((state?.actions ?? []).slice(0, 8));
	const hue = (t: 'ok' | 'warn' | 'err') => (t === 'ok' ? 'var(--bn-ok)' : t === 'err' ? 'var(--bn-err)' : 'var(--bn-warn)');
</script>

<SlabCard i={0} title="Auto-repair" sub={state?.lastPass ? `checks every 5 min · last ${relativeTime(state.lastPass)}` : 'starting'}>
	<div class="px-6 pb-6 pt-3">
		<div class="mb-3"><Chip tone={head.tone}>{head.text}</Chip></div>
		{#if rows.length}
			<ul class="flex flex-col gap-1.5" aria-label="auto-repair checks">
				{#each rows as r (r.id)}
					<li class="grid grid-cols-[10px_minmax(0,14rem)_6.5rem_1fr] items-baseline gap-3 text-[12px] max-[900px]:grid-cols-[10px_1fr]">
						<span class="inline-block h-2 w-2 rounded-full" style={`background: ${hue(r.tone)}`}></span>
						<span class="bn-text font-medium">{r.label}</span>
						<span class="font-mono text-[11px] max-[900px]:col-start-2" style={`color: ${hue(r.tone)}`}>{statusLabel(r.status)}</span>
						<span class="bn-dim max-[900px]:col-start-2">
							{r.detail}{#if r.action}<span class="bn-text block" class:font-semibold={r.status === 'needs_owner'}>{r.status === 'needs_owner' ? 'You: ' : ''}{r.action}</span>{/if}
						</span>
					</li>
				{/each}
			</ul>
		{/if}
		{#if actions.length}
			<div class="bn-dim mt-5 mb-1.5 font-mono text-[10px] uppercase tracking-wider">Recent repairs</div>
			<ul class="flex flex-col gap-1 text-[11.5px]">
				{#each actions as a (a.at + a.text)}
					<li class="bn-dim"><span class="font-mono">{relativeTime(a.at)}</span> · <span class="bn-text">{a.text}</span></li>
				{/each}
			</ul>
		{/if}
		{#if state?.holds?.length}
			<div class="bn-dim mt-5 mb-1.5 font-mono text-[10px] uppercase tracking-wider">Switched off on purpose (left alone)</div>
			<ul class="bn-dim flex flex-col gap-1 text-[11.5px]">
				{#each state.holds as h (h)}<li>{h}</li>{/each}
			</ul>
		{/if}
	</div>
</SlabCard>
