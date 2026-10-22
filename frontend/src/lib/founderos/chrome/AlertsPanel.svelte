<!--
  The bell drawer: turn on phone and desktop alerts for this device, choose
  which ones are worth a ping, set the morning brief hour, and have the open
  OS read alerts aloud. GET/POST/PUT/DELETE /api/founderos/pages/notify[...].
-->
<script lang="ts">
	import { onMount } from 'svelte';
	import { founderosFetch } from '$lib/founderos/api';
	import { Bell, Play, X } from '$lib/founderos/icons';
	import { Badge, Pressable } from '$lib/founderos/kit';
	import { speak, stopSpeaking } from '$lib/founderos/voice';
	import {
		alertsOpen,
		deviceLabel,
		hourLabel,
		keyBytes,
		needsHomeScreen,
		readSpeakAlerts,
		writeSpeakAlerts,
		type AlertStatus
	} from './alerts';

	let st = $state<AlertStatus | null>(null);
	let failure = $state<string | null>(null);
	let note = $state<string | null>(null);
	let busy = $state(false);
	let here = $state<string | null>(null); // this browser's push endpoint, if subscribed
	let supported = $state(true);
	let homeScreen = $state(false);
	let speakAlerts = $state(false);
	let briefing = $state(false);

	async function load() {
		try {
			const r = await founderosFetch<AlertStatus>('/pages/notify');
			if (!r?.kinds || !Array.isArray(r.devices)) throw new Error('unexpected reply');
			st = r;
			failure = null;
		} catch (e) {
			failure = e instanceof Error ? e.message : String(e);
		}
	}

	async function registration(): Promise<ServiceWorkerRegistration> {
		return (await navigator.serviceWorker.getRegistration('/')) ?? navigator.serviceWorker.register('/sw.js', { scope: '/' });
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
		}
	}

	const turnOn = () =>
		act(async () => {
			if (!st) return;
			const perm = await Notification.requestPermission();
			if (perm !== 'granted') throw new Error('Notifications are blocked for this site. Allow them in the browser settings, then try again.');
			const reg = await registration();
			await navigator.serviceWorker.ready;
			let sub = await reg.pushManager.getSubscription();
			if (!sub) sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: keyBytes(st.publicKey) as BufferSource });
			const json = sub.toJSON();
			st = await founderosFetch<AlertStatus>('/pages/notify/devices', {
				method: 'POST',
				json: { endpoint: json.endpoint, keys: json.keys, label: deviceLabel(navigator.userAgent) }
			});
			here = sub.endpoint;
			await founderosFetch('/pages/notify/test', { method: 'POST', json: {} });
			note = 'Done. A test alert is on its way to this device.';
		});

	const remove = (endpoint: string) =>
		act(async () => {
			st = await founderosFetch<AlertStatus>('/pages/notify/devices', { method: 'DELETE', json: { endpoint } });
			if (endpoint === here) {
				const sub = await (await registration()).pushManager.getSubscription();
				await sub?.unsubscribe();
				here = null;
			}
		});

	const test = () =>
		act(async () => {
			const r = await founderosFetch<{ sent: number }>('/pages/notify/test', { method: 'POST', json: {} });
			note = `Sent to ${r.sent} device${r.sent === 1 ? '' : 's'}.`;
		});

	const setKind = (kind: string, on: boolean) =>
		act(async () => {
			st = await founderosFetch<AlertStatus>('/pages/notify/prefs', { method: 'PUT', json: { kinds: { [kind]: on } } });
		});

	const setHour = (h: number) =>
		act(async () => {
			st = await founderosFetch<AlertStatus>('/pages/notify/prefs', { method: 'PUT', json: { briefHour: h } });
		});

	async function playBrief() {
		if (briefing) {
			stopSpeaking();
			briefing = false;
			return;
		}
		briefing = true;
		try {
			const r = await founderosFetch<{ text: string }>('/pages/notify/brief');
			await speak(r.text);
		} catch (e) {
			note = e instanceof Error ? e.message : String(e);
		} finally {
			briefing = false;
		}
	}

	onMount(() => {
		void load();
		speakAlerts = readSpeakAlerts();
		supported = 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window;
		const standalone = window.matchMedia?.('(display-mode: standalone)').matches || (navigator as { standalone?: boolean }).standalone === true;
		homeScreen = needsHomeScreen(navigator.userAgent, !!standalone);
		if (supported) {
			void navigator.serviceWorker
				.getRegistration('/')
				.then((reg) => reg?.pushManager.getSubscription())
				.then((sub) => (here = sub?.endpoint ?? null))
				.catch(() => {});
		}
	});

	const onHere = $derived(!!here && !!st?.devices.some((d) => d.endpoint === here));
</script>

<div class="fixed inset-0 z-50 flex justify-end" role="presentation">
	<button type="button" aria-label="Close alerts" class="absolute inset-0 bg-black/40" onclick={() => alertsOpen.set(false)}></button>
	<aside
		aria-label="Alerts"
		class="bn-alerts relative flex h-full w-full max-w-[400px] flex-col overflow-y-auto border-l text-[12.5px]"
	>
		<header class="bn-border flex items-center gap-2 border-b px-5 py-4">
			<Bell size={15} />
			<span class="font-semibold">Alerts &amp; voice</span>
			<button type="button" class="bn-dim ml-auto" aria-label="Close" onclick={() => alertsOpen.set(false)}><X size={15} /></button>
		</header>

		<div class="space-y-5 px-5 py-4">
			{#if !st}
				<p class="bn-dim font-mono text-[12px]">{failure ? `could not read your alerts: ${failure}` : 'reading…'}</p>
			{:else}
				<!-- this device -->
				<section class="space-y-2" data-part="device">
					<div class="flex items-center justify-between gap-2">
						<span class="font-semibold">Alerts on this device</span>
						<Badge tone={onHere ? 'ok' : 'default'}>{onHere ? 'on' : 'off'}</Badge>
					</div>
					{#if homeScreen}
						<div data-part="homescreen" class="bn-border space-y-1 rounded-[8px] border p-3">
							<p class="font-semibold">On iPhone, one step first:</p>
							<p>1. Tap the <b>Share</b> button at the bottom of Safari.</p>
							<p>2. Choose <b>Add to Home Screen</b>, then <b>Add</b>.</p>
							<p>3. Open <b>TFG OS</b> from your home screen, sign in, and tap the bell again.</p>
							<p class="bn-dim font-mono text-[10.5px]">Apple only lets home-screen apps send notifications.</p>
						</div>
					{:else if !supported}
						<p class="bn-dim">This browser can't receive notifications. Use Chrome or Safari.</p>
					{:else if !onHere}
						<p class="bn-muted">Get a notification when something needs you, even when the OS is closed.</p>
						<Pressable tone="primary" disabled={busy} onclick={turnOn}>{busy ? 'Turning on…' : 'Turn on alerts here'}</Pressable>
					{:else}
						<Pressable tone="secondary" disabled={busy} onclick={test}>Send a test</Pressable>
					{/if}
					{#if st.devices.length}
						<ul class="space-y-1">
							{#each st.devices as d (d.endpoint)}
								<li class="flex items-center gap-2 font-mono text-[11px]">
									<span class="truncate">{d.label}{d.endpoint === here ? ' (this one)' : ''}</span>
									<button type="button" class="bn-dim ml-auto underline" disabled={busy} onclick={() => remove(d.endpoint)}>remove</button>
								</li>
							{/each}
						</ul>
					{/if}
				</section>

				<!-- what's worth a ping -->
				<section class="bn-border space-y-2 border-t pt-4" data-part="kinds">
					<span class="font-semibold">Tell me when</span>
					{#each st.kinds as k (k.kind)}
						<label class="flex items-center gap-2">
							<input type="checkbox" checked={k.on} disabled={busy} onchange={(e) => setKind(k.kind, e.currentTarget.checked)} />
							<span>{k.label}</span>
						</label>
					{/each}
					<label class="flex items-center gap-2">
						<span class="bn-muted">Morning brief at</span>
						<select class="pc-input rounded-[6px] px-2 py-1 font-mono text-[11px]" value={st.briefHour} disabled={busy} onchange={(e) => setHour(Number(e.currentTarget.value))}>
							{#each Array.from({ length: 24 }, (_, i) => i) as h (h)}
								<option value={h}>{hourLabel(h)}</option>
							{/each}
						</select>
					</label>
				</section>

				<!-- voice -->
				<section class="bn-border space-y-2 border-t pt-4" data-part="voice">
					<div class="flex items-center justify-between gap-2">
						<span class="font-semibold">Voice</span>
						<Badge tone={st.voice.natural ? 'ok' : 'default'}>{st.voice.natural ? 'natural' : 'browser'}</Badge>
					</div>
					<p class="bn-dim font-mono text-[11px]">{st.voice.detail}</p>
					<label class="flex items-center gap-2">
						<input
							type="checkbox"
							checked={speakAlerts}
							onchange={(e) => {
								speakAlerts = e.currentTarget.checked;
								writeSpeakAlerts(speakAlerts);
							}}
						/>
						<span>Read alerts out loud while the OS is open</span>
					</label>
					<Pressable tone="secondary" onclick={playBrief}>
						<span class="inline-flex items-center gap-1.5"><Play size={12} />{briefing ? 'Stop' : "Play today's brief"}</span>
					</Pressable>
					<p class="bn-dim text-[11px]">To talk with the Conductor hands-free, open it (the robot button) and press the headphones.</p>
				</section>

				{#if note}<p data-part="note" class="font-mono text-[11px]">{note}</p>{/if}
			{/if}
		</div>
	</aside>
</div>

<style>
	.bn-alerts {
		border-color: var(--bn-border);
		background: var(--bn-bg);
		color: var(--bn-text);
	}
</style>
