<!--
  Pick the OS's voice: every voice on the ElevenLabs account, filtered by
  accent and gender, each with its free sample (no characters spent). The
  choice is yours only. GET /pages/voice/voices, PUT /pages/voice.
-->
<script lang="ts">
	import { onMount } from 'svelte';
	import { founderosFetch } from '$lib/founderos/api';
	import { Check, Play, Square } from '$lib/founderos/icons';
	import { speak } from '$lib/founderos/voice';
	import { filterVoices, type VoiceList } from './alerts';

	let list = $state<VoiceList | null>(null);
	let failure = $state<string | null>(null);
	let accent = $state('american');
	let gender = $state('female');
	let playing = $state<string | null>(null);
	let busy = $state(false);
	let sample: HTMLAudioElement | null = null;

	onMount(() => {
		void load();
		return () => sample?.pause();
	});

	async function load() {
		try {
			const r = await founderosFetch<VoiceList>('/pages/voice/voices');
			if (!Array.isArray(r?.voices)) throw new Error('unexpected reply');
			list = r;
			failure = r.error ?? null;
		} catch (e) {
			failure = e instanceof Error ? e.message : String(e);
		}
	}

	function play(id: string, url: string) {
		sample?.pause();
		if (playing === id) {
			playing = null;
			return;
		}
		sample = new Audio(url);
		playing = id;
		sample.onended = () => (playing = null);
		sample.play().catch(() => (playing = null));
	}

	async function choose(id: string, name: string) {
		busy = true;
		try {
			list = await founderosFetch<VoiceList>('/pages/voice', { method: 'PUT', json: { voiceId: id } });
			void speak(`Hi, I'm ${name}. This is how the OS will sound now.`);
		} catch (e) {
			failure = e instanceof Error ? e.message : String(e);
		} finally {
			busy = false;
		}
	}

	const shown = $derived(list ? filterVoices(list.voices, accent, gender) : []);
	const current = $derived(list?.voices.find((v) => v.id === list?.current));
</script>

<div class="space-y-2" data-part="voice-picker">
	{#if current}<p class="text-[12px]">Speaking as <b>{current.name}</b> <span class="bn-dim">· {current.description}</span></p>{/if}
	<div class="flex flex-wrap gap-2">
		<select aria-label="Accent" bind:value={accent} class="pc-input rounded-[6px] px-2 py-1 font-mono text-[11px]">
			<option value="american">American</option>
			<option value="british">British</option>
			<option value="">Any accent</option>
		</select>
		<select aria-label="Gender" bind:value={gender} class="pc-input rounded-[6px] px-2 py-1 font-mono text-[11px]">
			<option value="female">Female</option>
			<option value="male">Male</option>
			<option value="">Any</option>
		</select>
	</div>
	{#if failure}<p class="bn-dim font-mono text-[11px]">{failure}</p>{/if}
	{#if list}
		<ul class="space-y-1">
			{#each shown as v (v.id)}
				<li class="bn-border flex items-center gap-2 rounded-[6px] border px-2 py-1.5">
					{#if v.preview}
						<button type="button" aria-label={playing === v.id ? `Stop ${v.name}` : `Play ${v.name}`} class="bn-dim shrink-0" onclick={() => play(v.id, v.preview)}>
							{#if playing === v.id}<Square size={12} />{:else}<Play size={12} />{/if}
						</button>
					{/if}
					<span class="min-w-0 flex-1">
						<span class="font-semibold">{v.name}</span>{#if v.library}<span class="bn-dim ml-1.5 font-mono text-[10px]">needs paid plan</span>{/if}
						<span class="bn-dim block truncate text-[11px]">{v.description}{v.age ? ` · ${v.age}` : ''}</span>
					</span>
					{#if v.id === list.current}
						<span class="inline-flex shrink-0 items-center gap-1 font-mono text-[10.5px]"><Check size={12} />in use</span>
					{:else}
						<button type="button" class="shrink-0 font-mono text-[10.5px] underline" disabled={busy} onclick={() => choose(v.id, v.name)}>use</button>
					{/if}
				</li>
			{:else}
				<li class="bn-dim text-[11px]">No voice matches. Try another accent.</li>
			{/each}
		</ul>
	{/if}
</div>
