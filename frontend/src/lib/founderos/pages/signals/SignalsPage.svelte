<!--
  /os/signals: the pipeline ranked by why each prospect is worth contacting
  now (open sales roles on their job board, a recent round, no sales system on
  their website, already talking to us). Pick one to see the evidence and the
  openers; copy the LinkedIn note, the email opener or the call opener, and
  record the touch. Lusha fills a missing email or phone, one credit a press.
-->
<script lang="ts">
	import { onMount } from 'svelte';
	import { founderosFetch } from '$lib/founderos/api';
	import { Badge, Pressable, SlabCard } from '$lib/founderos/kit';
	import { ago, heat, matches, type Filter, type Row, type SignalsBoard } from './signals';

	let board = $state<SignalsBoard | null>(null);
	let failure = $state<string | null>(null);
	let note = $state<string | null>(null);
	let busy = $state(false);
	let filter = $state<Filter>('all');
	let picked = $state<string | null>(null);
	let copied = $state<string | null>(null);

	const fail = (e: unknown) => (e instanceof Error ? e.message : String(e));
	const rows = $derived(board ? board.rows.filter((r) => matches(r, filter)) : []);
	const sel = $derived<Row | null>(board?.rows.find((r) => r.contactId === picked) ?? rows[0] ?? null);

	async function load() {
		try {
			const r = await founderosFetch<SignalsBoard>('/pages/signals');
			if (!r || !Array.isArray(r.rows)) throw new Error('unexpected reply');
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

	const checkNow = (contactId?: string) =>
		act(async () => {
			const r = await founderosFetch<{ checked: number }>('/pages/signals/check', { method: 'POST', json: contactId ? { contactId } : {} });
			note = `Checked ${r.checked} prospect${r.checked === 1 ? '' : 's'}.`;
			await load();
		});

	const enrich = (r: Row) =>
		act(async () => {
			const res = await founderosFetch<{ state?: string; error?: string; email?: string; phone?: string; found?: boolean }>('/pages/signals/enrich', {
				method: 'POST',
				json: { contactId: r.contactId }
			});
			if (res.state === 'not_configured') {
				note = 'Lusha is not connected yet: add the API key in Connections.';
				return;
			}
			note = res.found ? `Found ${[res.email, res.phone].filter(Boolean).join(' and ')}.` : 'Lusha had no email or phone for them.';
			await load();
		});

	const touched = (r: Row, channel: 'linkedin' | 'email') =>
		act(async () => {
			await founderosFetch('/pages/signals/touched', { method: 'POST', json: { contactId: r.contactId, channel } });
			note = channel === 'linkedin' ? 'Noted: LinkedIn note sent. It comes back in three days.' : 'Noted: email sent. It comes back in three days.';
			await load();
		});

	async function copy(key: string, text?: string) {
		if (!text) return;
		try {
			await navigator.clipboard.writeText(text);
			copied = key;
			setTimeout(() => (copied = copied === key ? null : copied), 1500);
		} catch {
			note = 'Could not copy; select the text instead.';
		}
	}

	onMount(load);

	const FILTERS: { id: Filter; label: string }[] = [
		{ id: 'all', label: 'All' },
		{ id: 'hot', label: 'Hot' },
		{ id: 'hiring', label: 'Hiring sales' },
		{ id: 'funded', label: 'Recently funded' },
		{ id: 'unchecked', label: 'Not checked' }
	];
	const toneOf = (s: number) => (heat(s) === 'hot' ? 'ok' : heat(s) === 'warm' ? 'warn' : 'default');
</script>

{#if failure}
	<p class="bn-dim px-1 py-4 font-mono text-[11px]" data-part="failure">Signals unavailable: {failure}</p>
{:else if !board}
	<p class="bn-dim px-1 py-4 font-mono text-[11px]">Loading…</p>
{:else}
	<div class="grid grid-cols-2 gap-3 font-mono text-[11px] sm:grid-cols-5" data-part="stats">
		<div><div class="text-[18px] font-semibold">{board.stats.Hot}</div><div class="bn-dim">hot now</div></div>
		<div><div class="text-[18px] font-semibold">{board.stats.Hiring}</div><div class="bn-dim">hiring sales</div></div>
		<div><div class="text-[18px] font-semibold">{board.stats.Funded}</div><div class="bn-dim">funded lately</div></div>
		<div><div class="text-[18px] font-semibold">{board.stats.Checked}/{board.stats.Open}</div><div class="bn-dim">checked</div></div>
		<div><div class="text-[18px] font-semibold">{board.stats.NoEmail}</div><div class="bn-dim">no email yet</div></div>
	</div>

	<div class="mt-4 flex flex-wrap items-center gap-2">
		{#each FILTERS as f (f.id)}
			<Pressable tone={filter === f.id ? 'primary' : 'secondary'} onclick={() => (filter = f.id)}>{f.label}</Pressable>
		{/each}
		<span class="flex-1"></span>
		<Pressable tone="secondary" disabled={busy} onclick={() => checkNow()}>{busy ? 'Working…' : 'Check the next 6 now'}</Pressable>
	</div>
	<p class="bn-dim mt-2 font-mono text-[10.5px]" data-part="status">
		{board.autoChecks ? 'Checks every prospect about once a week by itself.' : 'Automatic checks are off (FOUNDEROS_CRONS); use the button.'}
		{#if board.lusha.configured}Lusha: {board.lusha.usedToday}/{board.lusha.dailyCap} lookups today.{:else}Lusha not connected: emails and phones can't be found yet (<a class="underline" href="/os/integrations">Connections</a>).{/if}
		{#if board.outreachPaused}Automatic outreach is paused, so nothing is emailed by itself.{/if}
	</p>
	{#if note}<p class="mt-2 font-mono text-[11px]" data-part="note">{note}</p>{/if}

	<div class="mt-4 grid gap-5 lg:grid-cols-[1fr_420px]">
		<SlabCard i={1} title="Who to contact" sub={`${rows.length} of ${board.rows.length}, strongest reason first`}>
			<ul class="divide-y px-3 pb-3 pt-1 text-[12px]" data-part="list">
				{#each rows as r (r.contactId)}
					<li>
						<button type="button" class="flex w-full items-center gap-3 px-3 py-2 text-left" class:font-semibold={sel?.contactId === r.contactId}
							onclick={() => (picked = r.contactId)} aria-pressed={sel?.contactId === r.contactId}>
							<Badge tone={toneOf(r.score)}>{r.score}</Badge>
							<span class="min-w-0 flex-1">
								<span class="block truncate">{r.company}{r.person ? ` · ${r.person}` : ''}</span>
								<span class="bn-dim block truncate text-[11px]">{r.whyNow || (r.checkedAt ? 'no signal found' : 'not checked yet')}</span>
							</span>
							<span class="bn-dim shrink-0 font-mono text-[10px]">{r.plays.nextLabel}</span>
						</button>
					</li>
				{:else}
					<li class="bn-dim px-3 py-4">Nobody matches this filter.</li>
				{/each}
			</ul>
		</SlabCard>

		{#if sel}
			<SlabCard i={2} title={sel.company} sub={[sel.person, sel.role].filter(Boolean).join(' · ') || 'buyer not named yet'}>
				<div class="space-y-4 px-6 pb-6 pt-3 text-[12.5px]" data-part="detail">
					<div class="flex flex-wrap items-center gap-2">
						<Badge tone={toneOf(sel.score)}>score {sel.score}</Badge>
						{#if sel.plays.offer}<Badge tone="accent">{sel.plays.offer}</Badge>{/if}
						<span class="bn-dim font-mono text-[10.5px]">{ago(sel.checkedAt)}</span>
					</div>

					<ul class="space-y-1.5" data-part="signals">
						{#each sel.signals as s, i (i)}
							<li class="flex gap-2">
								<span class="bn-dim w-[52px] shrink-0 font-mono text-[10px] uppercase">{s.kind}</span>
								<span class="min-w-0 flex-1">
									{#if s.url}<a class="underline" href={s.url} target="_blank" rel="noopener noreferrer">{s.title}</a>{:else}{s.title}{/if}
									{#if s.detail}<span class="bn-dim"> · {s.detail}</span>{/if}
								</span>
							</li>
						{:else}
							<li class="bn-dim">No signals yet.</li>
						{/each}
					</ul>
					{#if sel.problem}<p class="bn-dim font-mono text-[10.5px]">Couldn't read: {sel.problem}</p>{/if}

					<div class="flex flex-wrap gap-2 font-mono text-[11px]">
						{#if sel.email}<span>{sel.email}</span>{/if}
						{#if sel.phone}<span>{sel.phone}</span>{/if}
						{#if sel.website}<a class="underline" href={sel.website} target="_blank" rel="noopener noreferrer">website</a>{/if}
						{#if sel.linkedin}<a class="underline" href={sel.linkedin} target="_blank" rel="noopener noreferrer">LinkedIn</a>{/if}
					</div>

					<div class="flex flex-wrap gap-2" data-part="actions">
						{#if sel.plays.next === 'reply'}<Pressable tone="primary" href="/os/comms">Reply and book</Pressable>{/if}
						{#if sel.phone}<Pressable tone={sel.plays.next === 'call' ? 'primary' : 'secondary'} href="/os/dialer">Call in the Dialer</Pressable>{/if}
						{#if !sel.email || !sel.phone}
							<Pressable tone={sel.plays.next === 'find_contact' ? 'primary' : 'secondary'} disabled={busy || !sel.person} onclick={() => enrich(sel!)}>Find email and phone (Lusha)</Pressable>
						{/if}
						<Pressable tone="secondary" disabled={busy} onclick={() => checkNow(sel!.contactId)}>Re-check</Pressable>
					</div>

					{#if sel.plays.linkedinNote}
						<div class="space-y-1.5" data-part="linkedin">
							<p class="font-semibold">LinkedIn note <span class="bn-dim font-mono text-[10px] font-normal">{sel.plays.linkedinNote.length}/300</span></p>
							<pre class="bn-border whitespace-pre-wrap rounded-[8px] border p-3 font-sans text-[12.5px] leading-relaxed">{sel.plays.linkedinNote}</pre>
							<div class="flex flex-wrap gap-2">
								<Pressable tone="secondary" onclick={() => copy('li', sel!.plays.linkedinNote)}>{copied === 'li' ? 'Copied' : 'Copy note'}</Pressable>
								{#if sel.linkedin}<Pressable tone="secondary" href={sel.linkedin} target="_blank" rel="noopener noreferrer">Open profile</Pressable>{/if}
								<Pressable tone="secondary" disabled={busy} onclick={() => touched(sel!, 'linkedin')}>I sent it</Pressable>
							</div>
						</div>
					{/if}
					{#if sel.plays.emailOpener}
						<div class="space-y-1.5" data-part="email">
							<p class="font-semibold">Email opener</p>
							<pre class="bn-border whitespace-pre-wrap rounded-[8px] border p-3 font-sans text-[12.5px] leading-relaxed">{sel.plays.emailOpener}</pre>
							<div class="flex flex-wrap gap-2">
								<Pressable tone="secondary" onclick={() => copy('em', sel!.plays.emailOpener)}>{copied === 'em' ? 'Copied' : 'Copy opener'}</Pressable>
								{#if sel.email}<Pressable tone="secondary" disabled={busy} onclick={() => touched(sel!, 'email')}>I emailed them</Pressable>{/if}
							</div>
						</div>
					{/if}
					{#if sel.plays.callOpener}
						<div class="space-y-1.5" data-part="call">
							<p class="font-semibold">Call opener</p>
							<pre class="bn-border whitespace-pre-wrap rounded-[8px] border p-3 font-sans text-[12.5px] leading-relaxed">{sel.plays.callOpener}</pre>
						</div>
					{/if}
				</div>
			</SlabCard>
		{/if}
	</div>
{/if}
