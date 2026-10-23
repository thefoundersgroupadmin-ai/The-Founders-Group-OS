<!--
  /os/dialer: ring prospects back to back. The OS picks who is next (in their
  calling hours, not a teammate's, not on the do-not-call list) and gives a
  30-second brief; the call runs in the browser; one tap (or a key 1–9)
  records how it went, moves the pipeline and, for a warm call, drafts the
  follow-up into the Outbox. "Keep dialling" rings the next one by itself.
-->
<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import { founderosFetch } from '$lib/founderos/api';
	import { Mic, MicOff, Phone, PhoneOff } from '$lib/founderos/icons';
	import { Badge, Pressable, SlabCard } from '$lib/founderos/kit';
	import { clock, FOLLOW_UP, nextMorning, OUTCOMES, prettyPhone, type Call, type DialerBoard, type Prospect } from './dialer';
	import { dial, shutdown, type LiveCall } from './phone';

	let board = $state<DialerBoard | null>(null);
	let failure = $state<string | null>(null);
	let note = $state<string | null>(null);
	let busy = $state(false);

	let prospect = $state<Prospect | null>(null);
	let empty = $state<string | null>(null);
	let call = $state<Call | null>(null);
	let live = $state<LiveCall | null>(null);
	let wrapUp = $state(false);
	let notes = $state('');
	let callbackAt = $state(nextMorning());
	let followUp = $state(true);
	let keepDialling = $state(false);
	let countdown = $state<number | null>(null);
	let elapsed = $state(0);
	let keypad = $state(false);
	let timer: ReturnType<typeof setInterval> | null = null;
	let countTimer: ReturnType<typeof setInterval> | null = null;

	const fail = (e: unknown) => (e instanceof Error ? e.message : String(e));

	async function load() {
		try {
			const r = await founderosFetch<DialerBoard>('/pages/dialer');
			if (!r?.twilio || !Array.isArray(r.queue)) throw new Error('unexpected reply');
			board = r;
			failure = null;
		} catch (e) {
			failure = fail(e);
		}
	}

	async function act(fn: () => Promise<void>) {
		busy = true;
		note = null;
		try {
			await fn();
		} catch (e) {
			note = fail(e);
		} finally {
			busy = false;
		}
	}

	const setup = () =>
		act(async () => {
			await founderosFetch('/pages/dialer/setup', { method: 'POST', json: {} });
			await load();
		});

	const enrich = () =>
		act(async () => {
			const r = await founderosFetch<{ requested: number; note: string }>('/pages/dialer/enrich', { method: 'POST', json: { limit: 10 } });
			note = `Asked Apollo about ${r.requested} contact${r.requested === 1 ? '' : 's'}. ${r.note}.`;
		});

	async function next(skip?: string) {
		stopCountdown();
		wrapUp = false;
		call = null;
		live = null;
		notes = '';
		callbackAt = nextMorning();
		followUp = true;
		keypad = false;
		await act(async () => {
			const r = await founderosFetch<{ prospect: Prospect | null; why?: string }>('/pages/dialer/next', { method: 'POST', json: skip ? { skip } : {} });
			prospect = r.prospect;
			empty = r.prospect ? null : (r.why ?? 'nobody to call right now');
			if (prospect && keepDialling) startCountdown();
		});
		void load();
	}

	function startCountdown() {
		stopCountdown();
		countdown = 3;
		countTimer = setInterval(() => {
			if (countdown === null) return;
			countdown -= 1;
			if (countdown <= 0) {
				stopCountdown();
				void place();
			}
		}, 1000);
	}

	function stopCountdown() {
		if (countTimer) clearInterval(countTimer);
		countTimer = null;
		countdown = null;
	}

	async function place() {
		if (!prospect || live) return;
		stopCountdown();
		await act(async () => {
			const r = await founderosFetch<{ call: Call }>('/pages/dialer/calls', { method: 'POST', json: { contactId: prospect!.contactId } });
			call = r.call;
			elapsed = 0;
			live = await dial(
				r.call.id,
				(c) => {
					live = c;
					if (c.state === 'open' && !timer) timer = setInterval(() => (elapsed += 1), 1000);
					if (c.state === 'ended') {
						if (timer) clearInterval(timer);
						timer = null;
						wrapUp = true;
					}
				},
				(m) => (note = `Call: ${m}`)
			);
		});
		if (!live && call) wrapUp = true; // the call never started: still record it
	}

	function hangUp() {
		live?.hangUp();
	}

	async function save(outcome: string) {
		if (!call) return;
		const id = call.id;
		const draft = followUp && FOLLOW_UP.has(outcome) && !!prospect?.email;
		await act(async () => {
			await founderosFetch(`/pages/dialer/calls/${id}/outcome`, {
				method: 'POST',
				json: { outcome, notes, callbackAt: outcome === 'callback' ? new Date(callbackAt).toISOString() : null }
			});
			if (draft) {
				// written in the background; it waits in the Outbox for a click
				void founderosFetch(`/pages/dialer/calls/${id}/followup`, { method: 'POST', json: {} }).catch((e) => (note = `Follow-up not drafted: ${fail(e)}`));
			}
		});
		if (!note) await next();
	}

	function onKey(e: KeyboardEvent) {
		const t = e.target as HTMLElement | null;
		if (t && (t.tagName === 'TEXTAREA' || t.tagName === 'INPUT')) return;
		if (wrapUp && !busy) {
			const o = OUTCOMES.find((x) => x.key === e.key);
			if (o && (o.id !== 'callback' || callbackAt)) {
				e.preventDefault();
				void save(o.id);
			}
		} else if (e.key === ' ' && prospect && !live && !wrapUp) {
			e.preventDefault();
			void place();
		}
	}

	onMount(() => {
		void load();
		window.addEventListener('keydown', onKey);
		return () => window.removeEventListener('keydown', onKey);
	});
	onDestroy(() => {
		stopCountdown();
		if (timer) clearInterval(timer);
		live?.hangUp();
		shutdown();
	});

	const onCall = $derived(!!live && live.state !== 'ended');
</script>

{#if !board}
	<p class="bn-dim font-mono text-[12px]">{failure ? `could not read the dialer: ${failure}` : 'reading…'}</p>
{:else}
	<!-- today -->
	<div class="mb-5 grid grid-cols-2 gap-3 sm:grid-cols-4" data-part="stats">
		{#each [['Dials', board.stats.dials], ['Conversations', board.stats.connects], ['Meetings', board.stats.meetings], ['Talk time', clock(board.stats.talkSeconds)]] as [label, value] (label)}
			<div class="bn-border rounded-[8px] border px-4 py-3">
				<p class="bn-dim font-mono text-[10px] uppercase tracking-wider">{label}</p>
				<p class="font-mono text-[20px] font-semibold">{value}</p>
			</div>
		{/each}
	</div>

	{#if !board.twilio.ready}
		<SlabCard i={0} title="Set up calling" sub="one time">
			<div class="space-y-2 px-6 pb-6 pt-3 text-[12.5px]" data-part="setup">
				{#if !board.twilio.configured}
					<ol class="list-decimal space-y-1 pl-5">
						<li>In Twilio, buy one US phone number (Phone Numbers → Buy a number).</li>
						<li>In <a class="underline" href="/os/integrations">Connections</a>, open <b>Twilio</b> and paste the Account SID and Auth Token from the Twilio console's home page.</li>
						<li>Come back here and press <b>Finish setup</b>.</li>
					</ol>
				{:else}
					<p>Twilio is connected. Finishing setup makes the OS's own calling key and app in your Twilio account and picks your number.</p>
				{/if}
				<Pressable tone="primary" disabled={busy || !board.twilio.configured} onclick={setup}>{busy ? 'Setting up…' : 'Finish setup'}</Pressable>
			</div>
		</SlabCard>
	{/if}

	<div class="mt-5 grid gap-5 lg:grid-cols-[1fr_320px]">
		<!-- the call -->
		<SlabCard i={1} title={prospect ? firstName(prospect) : 'Dialler'} sub={prospect ? `${prospect.company}${prospect.localTime ? ` · ${prospect.localTime} there` : ''}` : `calling from ${board.twilio.callerId ? prettyPhone(board.twilio.callerId) : '—'}`}>
			<div class="space-y-4 px-6 pb-6 pt-3 text-[12.5px]" data-part="call">
				{#if !prospect}
					{#if empty}<p class="bn-dim" data-part="empty">{empty}</p>{/if}
					<div class="flex flex-wrap items-center gap-3">
						<Pressable tone="primary" disabled={busy || !board.twilio.ready} onclick={() => next()}>{busy ? 'Finding…' : 'Start dialling'}</Pressable>
						<label class="flex items-center gap-2"><input type="checkbox" bind:checked={keepDialling} /> Keep dialling (rings the next one by itself)</label>
					</div>
				{:else}
					<div class="flex flex-wrap items-center gap-2">
						<span class="font-mono text-[15px] font-semibold">{prettyPhone(prospect.phone)}</span>
						{#if prospect.role}<Badge>{prospect.role}</Badge>{/if}
						{#if prospect.callbackDue}<Badge tone="warn">callback due</Badge>{/if}
						{#if prospect.attempts}<span class="bn-dim font-mono text-[10.5px]">try {prospect.attempts + 1}{prospect.lastOutcome ? ` · last: ${prospect.lastOutcome.replace('_', ' ')}` : ''}</span>{/if}
					</div>
					{#if prospect.brief}
						<pre class="bn-border whitespace-pre-wrap rounded-[8px] border p-3 font-sans text-[12.5px] leading-relaxed" data-part="brief">{prospect.brief}</pre>
					{/if}

					{#if !live && !wrapUp}
						<div class="flex flex-wrap items-center gap-3">
							<Pressable tone="primary" disabled={busy} onclick={place}>
								<span class="inline-flex items-center gap-1.5"><Phone size={13} />{countdown !== null ? `Calling in ${countdown}…` : 'Call (space)'}</span>
							</Pressable>
							{#if countdown !== null}<Pressable tone="secondary" onclick={stopCountdown}>Wait</Pressable>{/if}
							<Pressable tone="secondary" disabled={busy} onclick={() => next(prospect!.contactId)}>Skip</Pressable>
							<label class="flex items-center gap-2"><input type="checkbox" bind:checked={keepDialling} /> Keep dialling</label>
						</div>
					{:else if onCall}
						<div class="flex flex-wrap items-center gap-3" data-part="live">
							<Badge tone={live?.state === 'open' ? 'ok' : 'default'}>{live?.state === 'open' ? `on the call · ${clock(elapsed)}` : live?.state === 'ringing' ? 'ringing…' : 'connecting…'}</Badge>
							<Pressable tone="secondary" onclick={() => live?.mute(!live.muted)}>
								<span class="inline-flex items-center gap-1.5">{#if live?.muted}<MicOff size={13} />Unmute{:else}<Mic size={13} />Mute{/if}</span>
							</Pressable>
							<Pressable tone="secondary" onclick={() => (keypad = !keypad)}>Keypad</Pressable>
							<Pressable tone="primary" onclick={hangUp}><span class="inline-flex items-center gap-1.5"><PhoneOff size={13} />Hang up</span></Pressable>
						</div>
						{#if keypad}
							<div class="grid w-[180px] grid-cols-3 gap-1.5" data-part="keypad">
								{#each ['1', '2', '3', '4', '5', '6', '7', '8', '9', '*', '0', '#'] as k (k)}
									<button type="button" class="bn-border rounded-[6px] border py-1.5 font-mono" onclick={() => live?.digits(k)}>{k}</button>
								{/each}
							</div>
						{/if}
					{/if}

					{#if live || wrapUp}
						<textarea bind:value={notes} rows="3" placeholder="Notes: who you spoke to, what they said, next step" aria-label="Call notes"
							class="pc-input w-full rounded-[6px] px-2.5 py-2 text-[12.5px]"></textarea>
					{/if}

					{#if wrapUp}
						<div class="space-y-2" data-part="wrapup">
							<p class="font-semibold">How did it go? <span class="bn-dim font-mono text-[10.5px] font-normal">press 1–9</span></p>
							<div class="flex flex-wrap gap-2">
								{#each OUTCOMES as o (o.id)}
									<Pressable tone={o.id === 'meeting' ? 'primary' : 'secondary'} disabled={busy} onclick={() => save(o.id)}>{o.key} · {o.label}</Pressable>
								{/each}
							</div>
							<label class="flex flex-wrap items-center gap-2">
								<span class="bn-muted">Call back at</span>
								<input type="datetime-local" bind:value={callbackAt} class="pc-input rounded-[6px] px-2 py-1 font-mono text-[11px]" aria-label="Call back at" />
							</label>
							{#if prospect.email}
								<label class="flex items-center gap-2"><input type="checkbox" bind:checked={followUp} /> Draft the follow-up email (meeting, interested, call back) into the Outbox</label>
							{/if}
						</div>
					{/if}
				{/if}
				{#if note}<p class="font-mono text-[11px]" data-part="note">{note}</p>{/if}
			</div>
		</SlabCard>

		<!-- up next -->
		<SlabCard i={2} title="Up next" sub={`${board.queue.length} in their calling hours now`}>
			<div class="space-y-3 px-6 pb-6 pt-3 text-[12px]" data-part="queue">
				{#if board.queue.length === 0}
					<p class="bn-dim">Nobody is callable right now.</p>
				{:else}
					<ul class="space-y-1.5">
						{#each board.queue.slice(0, 8) as q (q.contactId)}
							<li class="flex items-center gap-2">
								<span class="min-w-0 flex-1 truncate">{firstName(q)} <span class="bn-dim">· {q.company}</span></span>
								<span class="bn-dim shrink-0 font-mono text-[10.5px]">{q.localTime}</span>
							</li>
						{/each}
					</ul>
				{/if}
				<div class="bn-border space-y-1.5 border-t pt-3">
					<p class="bn-muted">{board.contacts.withPhone} open contacts have a number · {board.contacts.noPhone} don't</p>
					{#if board.apollo.configured}
						<Pressable tone="secondary" disabled={busy || board.contacts.noPhone === 0} onclick={enrich}>Find numbers with Apollo (10)</Pressable>
					{:else}
						<p class="bn-dim">Connect <a class="underline" href="/os/integrations">Apollo</a> to find direct dials and mobiles.</p>
					{/if}
				</div>
				<p class="bn-dim text-[10.5px]">Calls go out only weekdays 8am–6pm in the prospect's own time. "Do not call" and "Wrong number" are never rung again.</p>
			</div>
		</SlabCard>
	</div>
{/if}

<script module lang="ts">
	function firstName(p: { person?: string; name: string }): string {
		return (p.person || p.name || '').trim() || 'Prospect';
	}
</script>
