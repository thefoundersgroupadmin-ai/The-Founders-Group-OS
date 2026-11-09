<!-- /os/pods: client AI SDR pods (docs/tfg/CLIENT-PODS.md). One row per client;
     the owner creates a pod here. Data: GET/POST /api/founderos/pods. -->
<script lang="ts">
	import { onMount } from 'svelte';
	import { founderosFetch } from '$lib/founderos/api';
	import { Badge, PageHeader, Pressable, Rise, Slab } from '$lib/founderos/kit';
	import { STATUS_BADGE } from '$lib/founderos/pages/pods/pods';
	import type { Pod } from '$lib/founderos/pages/pods/types';

	let pods = $state<Pod[] | null>(null);
	let alerts = $state<Record<string, number>>({});
	let failure = $state<string | null>(null);
	let form = $state({ clientName: '', package: 'custom', reps: 4 });
	let note = $state('');
	let busy = $state(false);

	function load() {
		founderosFetch<{ pods: Pod[]; alerts?: Record<string, number> }>('/pods')
			.then((b) => {
				pods = b.pods;
				alerts = b.alerts ?? {};
			})
			.catch((e: unknown) => (failure = e instanceof Error ? e.message : 'unreachable'));
	}
	onMount(load);

	async function create() {
		busy = true;
		note = '';
		try {
			const r = await founderosFetch<{ pod: Pod }>('/pods', { method: 'POST', json: form });
			form = { clientName: '', package: 'custom', reps: 4 };
			location.href = `/os/pods/${r.pod.slug}`;
		} catch (e: unknown) {
			note = e instanceof Error ? e.message : 'could not create the pod';
		} finally {
			busy = false;
		}
	}
</script>

<div>
	<PageHeader eyebrow="clients" title="Client AI SDRs">
		{#snippet right()}
			{#if pods}<span class="bn-muted font-mono text-[10px]">{pods.length} clients</span>{/if}
		{/snippet}
	</PageHeader>
	<Rise i={1}>
		<Slab>
			<p class="bn-dim mb-2 font-mono text-[10px] uppercase tracking-[0.15em]">New client</p>
			<div class="flex flex-wrap items-center gap-2">
				<input bind:value={form.clientName} placeholder="Client name" aria-label="Client name" class="pc-input min-w-[220px] flex-1 rounded-[6px] px-2.5 py-1.5 text-[12px]" />
				<select bind:value={form.package} aria-label="Package" class="pc-input rounded-[6px] px-2 py-1.5 text-[12px]">
					<option value="custom">Custom</option><option value="core">Core</option><option value="full">The Full</option>
				</select>
				<input type="number" min="1" max="200" bind:value={form.reps} aria-label="Sales reps" class="pc-input w-[80px] rounded-[6px] px-2 py-1.5 text-[12px]" />
				<Pressable tone="primary" disabled={busy || form.clientName.trim() === ''} onclick={create}>{busy ? 'Creating…' : 'Create pod'}</Pressable>
			</div>
			{#if note}<p class="mt-2 font-mono text-[11px]" style="color: var(--bn-err)">{note}</p>{/if}
		</Slab>
		<div class="mt-3 flex flex-col gap-2">
			{#if pods}
				{#if pods.length === 0}
					<Slab><p class="bn-dim font-mono text-[12px]">No clients yet. Create a pod to set up a client's AI SDR.</p></Slab>
				{/if}
				{#each pods as p (p.slug)}
					<a href={`/os/pods/${p.slug}`} class="block">
						<Slab>
							<div class="flex flex-wrap items-center justify-between gap-2">
								<div>
									<p class="text-[14px] font-semibold">{p.clientName}</p>
									<p class="bn-dim font-mono text-[11px]">{p.package} · {p.reps} reps · {p.senderEmail || 'no sender yet'}</p>
								</div>
								<div class="flex items-center gap-2">
									{#if alerts[p.slug]}<Badge tone="err">{alerts[p.slug]} need{alerts[p.slug] === 1 ? 's' : ''} attention</Badge>{/if}
									<Badge tone={STATUS_BADGE[p.status].tone}>{STATUS_BADGE[p.status].label}</Badge>
								</div>
							</div>
						</Slab>
					</a>
				{/each}
			{:else if failure}
				<Slab><p class="font-mono text-[12px]" style="color: var(--bn-err)">Client pods could not be read: {failure}</p></Slab>
			{:else}
				<Slab><p class="bn-dim animate-pulse font-mono text-[12px]">loading clients…</p></Slab>
			{/if}
		</div>
	</Rise>
</div>
