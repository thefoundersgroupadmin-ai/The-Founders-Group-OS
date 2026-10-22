<!--
  Runs in the /os layout: when "read alerts out loud" is on for this browser,
  reads /pages/notify/feed every 30s and says each new alert. A morning brief
  notification opens /os?brief=1, which offers the brief as one tap (browsers
  only play sound after a tap on the page).
-->
<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { founderosFetch } from '$lib/founderos/api';
	import { Play, X } from '$lib/founderos/icons';
	import { speak, stopSpeaking } from '$lib/founderos/voice';
	import { readSpeakAlerts, spokenLine, type AlertEvent } from './alerts';

	let dismissed = $state(false);
	let playing = $state(false);
	const offerBrief = $derived(page.url.searchParams.get('brief') === '1' && !dismissed);

	async function playBrief() {
		if (playing) {
			stopSpeaking();
			playing = false;
			return;
		}
		playing = true;
		try {
			const r = await founderosFetch<{ text: string }>('/pages/notify/brief');
			await speak(r.text);
		} finally {
			playing = false;
			dismissed = true;
		}
	}

	onMount(() => {
		let after = new Date().toISOString();
		let queue: Promise<void> = Promise.resolve();
		const tick = async () => {
			if (!readSpeakAlerts() || document.visibilityState !== 'visible') return;
			try {
				const r = await founderosFetch<{ now: string; events: AlertEvent[] }>(`/pages/notify/feed?after=${encodeURIComponent(after)}`);
				if (!r || !Array.isArray(r.events)) return;
				after = r.now;
				for (const e of r.events) queue = queue.then(() => speak(spokenLine(e)));
			} catch {
				/* offline for a moment: the next tick catches up */
			}
		};
		const t = setInterval(tick, 30_000);
		return () => clearInterval(t);
	});
</script>

{#if offerBrief}
	<div class="bn-brief fixed bottom-5 right-5 z-40 flex items-center gap-2 rounded-[10px] border px-3 py-2 text-[12px]" data-part="brief-offer">
		<button type="button" class="inline-flex items-center gap-1.5 font-semibold" onclick={playBrief}>
			<Play size={13} />{playing ? 'Stop' : 'Play your morning brief'}
		</button>
		<button type="button" class="bn-dim" aria-label="Dismiss" onclick={() => (dismissed = true)}><X size={13} /></button>
	</div>
{/if}

<style>
	.bn-brief {
		border-color: var(--bn-border-strong);
		background: var(--bn-surface);
		color: var(--bn-text);
	}
</style>
