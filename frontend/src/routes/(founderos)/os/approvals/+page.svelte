<!-- /os/approvals: everything the agent board is waiting on the operator
     for, so nothing needs the Paperclip UI. Review requests (accept / send
     back with a reason), agent questions (pick and submit), the task's
     documents and thread for context, and an instruction box on the task.
     Data: GET /api/founderos/pages/approvals; decisions are guarded writes. -->
<script lang="ts">
	import { onMount } from 'svelte';
	import { founderosFetch } from '$lib/founderos/api';
	import { Badge, PageHeader, Pressable, Rise, Slab } from '$lib/founderos/kit';
	import Markdown from '$lib/founderos/pages/agents/Markdown.svelte';
	import { answersOf, canReject, checkboxesValid, choose, labelsOf, toggleIn, verdictsOf } from '$lib/founderos/pages/approvals/approvals';
	import type { ApprovalItem, ApprovalsBody, TaskContext, VerdictValue } from '$lib/founderos/pages/approvals/types';

	let data = $state<ApprovalsBody | null>(null);
	let failure = $state<string | null>(null);
	let reasons = $state<Record<string, string>>({});
	let picked = $state<Record<string, Record<string, string[]>>>({});
	let instructions = $state<Record<string, string>>({});
	let contexts = $state<Record<string, TaskContext | 'loading' | string>>({});
	let busy = $state<Record<string, boolean>>({});
	let notes = $state<Record<string, string>>({});
	let texts = $state<Record<string, Record<string, string>>>({});
	let selections = $state<Record<string, string[]>>({});
	let verdictPicks = $state<Record<string, Record<string, VerdictValue>>>({});
	let verdictReasons = $state<Record<string, Record<string, string>>>({});
	const VERDICT_LABEL: Record<VerdictValue, string> = { approve: 'Approve', reject: 'Reject', defer: 'Later' };

	function load() {
		founderosFetch<ApprovalsBody>('/pages/approvals')
			.then((b) => {
				data = b;
				for (const it of b.items) {
					// Pre-tick what the agent proposed: every suggested task, the default boxes.
					if (it.kind === 'suggest_tasks') selections[it.id] ??= it.tasks.map((t) => t.clientKey);
					if (it.kind === 'request_checkbox_confirmation') selections[it.id] ??= [...it.defaultSelected];
				}
			})
			.catch((e: unknown) => (failure = e instanceof Error ? e.message : 'unreachable'));
	}
	onMount(load);

	function showWork(it: ApprovalItem) {
		if (contexts[it.issueId] && contexts[it.issueId] !== 'loading') {
			delete contexts[it.issueId];
			return;
		}
		contexts[it.issueId] = 'loading';
		founderosFetch<TaskContext>(`/pages/approvals/issues/${encodeURIComponent(it.issueId)}`)
			.then((c) => (contexts[it.issueId] = c))
			.catch((e: unknown) => (contexts[it.issueId] = e instanceof Error ? e.message : 'could not load the work'));
	}

	async function act(it: ApprovalItem, path: string, json: unknown, done: string) {
		busy[it.id] = true;
		notes[it.id] = '';
		try {
			await founderosFetch(path, { method: 'POST', json });
			notes[it.id] = done;
			load();
		} catch (e: unknown) {
			notes[it.id] = e instanceof Error ? e.message : 'request failed';
		} finally {
			busy[it.id] = false;
		}
	}

	const base = (it: ApprovalItem) =>
		`/pages/approvals/issues/${encodeURIComponent(it.issueId)}/interactions/${encodeURIComponent(it.id)}`;
	const accept = (it: ApprovalItem) => {
		const choice =
			it.kind === 'suggest_tasks'
				? { selectedClientKeys: selections[it.id] ?? [] }
				: it.kind === 'request_checkbox_confirmation'
					? { selectedOptionIds: selections[it.id] ?? [] }
					: {};
		return act(it, `${base(it)}/accept`, choice, 'Approved.');
	};
	const submitVerdicts = (it: ApprovalItem, verdicts: unknown) => act(it, `${base(it)}/verdicts`, { verdicts }, 'Verdicts sent.');
	const reject = (it: ApprovalItem) => act(it, `${base(it)}/reject`, { reason: (reasons[it.id] ?? '').trim() }, 'Sent back to the agent.');
	const respond = (it: ApprovalItem, answers: unknown) => act(it, `${base(it)}/respond`, { answers }, 'Answer sent.');
	async function instruct(it: ApprovalItem) {
		const message = (instructions[it.id] ?? '').trim();
		if (!message) return;
		await act(it, `/pages/approvals/issues/${encodeURIComponent(it.issueId)}/comment`, { message }, 'Instruction sent to the agent.');
		instructions[it.id] = '';
	}
	function toggle(it: ApprovalItem, qi: number, optionId: string) {
		const q = it.questions[qi];
		picked[it.id] ??= {};
		picked[it.id][q.id] = choose(q, picked[it.id][q.id] ?? [], optionId);
	}
</script>

<div>
	<PageHeader eyebrow="waiting on you" title="Approvals">
		{#snippet right()}
			{#if data}
				<span class="bn-muted font-mono text-[10px]">{data.items.length} from agents</span>
			{/if}
		{/snippet}
	</PageHeader>
	<Rise i={1}>
		{#if data}
			{#if data.outboxPending > 0}
				<Slab>
					<p class="font-mono text-[12px]">
						{data.outboxPending} email{data.outboxPending === 1 ? '' : 's'} waiting to send:
						<a href="/os/outbox" class="underline underline-offset-2">open the Outbox</a>
					</p>
				</Slab>
			{/if}
			{#if data.warnings.length > 0}
				<Slab>
					<p class="font-mono text-[11px]" style="color: var(--bn-warn)">Some tasks could not be checked, so this list may be incomplete:</p>
					{#each data.warnings as w (w)}<p class="bn-dim font-mono text-[10.5px]">{w}</p>{/each}
				</Slab>
			{/if}
			{#if data.boardError}
				<Slab><p class="font-mono text-[12px]" style="color: var(--bn-err)">The agent board could not be read: {data.boardError}</p></Slab>
			{:else if data.items.length === 0}
				<Slab><p class="bn-dim font-mono text-[12px]">Nothing waiting. Agent review requests and questions appear here.</p></Slab>
			{/if}
			<div class="mt-3 flex flex-col gap-3">
				{#each data.items as it (it.id)}
					{@const labels = labelsOf(it)}
					{@const ctx = contexts[it.issueId]}
					<Slab>
						<div class="flex flex-wrap items-center justify-between gap-2">
							<p class="font-mono text-[12px]">{it.issueIdentifier} · {it.issueTitle}</p>
							<Badge tone={it.kind === 'ask_user_questions' ? 'warn' : 'accent'}
								>{({ ask_user_questions: 'Question', suggest_tasks: 'Suggested tasks', request_checkbox_confirmation: 'Checklist', request_item_verdicts: 'Verdicts' } as Record<string, string>)[it.kind] ?? 'Review'}</Badge
							>
						</div>
						<p class="mt-2 text-[13px] font-semibold">{it.title || 'Review requested'}</p>
						{#if it.prompt}<p class="bn-muted mt-1 text-[12.5px]">{it.prompt}</p>{/if}
						{#if it.details}<div class="mt-2"><Markdown text={it.details} /></div>{/if}

						<div class="mt-2">
							<Pressable tone="secondary" onclick={() => showWork(it)}>{ctx && ctx !== 'loading' ? 'Hide the work' : 'Show the work'}</Pressable>
						</div>
						{#if ctx === 'loading'}
							<p class="bn-dim mt-2 animate-pulse font-mono text-[11px]">loading the task…</p>
						{:else if typeof ctx === 'string'}
							<p class="mt-2 font-mono text-[11px]" style="color: var(--bn-err)">{ctx}</p>
						{:else if ctx}
							{#each ctx.documents as doc (doc.key)}
								<details class="pc-panel mt-2 rounded-[10px] p-2.5">
									<summary class="cursor-pointer font-mono text-[11px]">{doc.title || doc.key}</summary>
									<div class="mt-2"><Markdown text={doc.body} /></div>
								</details>
							{/each}
							{#if ctx.thread.length > 0}
								<details class="pc-panel mt-2 rounded-[10px] p-2.5">
									<summary class="cursor-pointer font-mono text-[11px]">Recent activity ({ctx.thread.length})</summary>
									{#each ctx.thread as cm (cm.id)}
										<div class="mt-2 border-t pt-2"><Markdown text={cm.body} /></div>
									{/each}
								</details>
							{/if}
						{/if}

						{#if it.kind === 'ask_user_questions'}
							{#each it.questions as q, qi (q.id)}
								<p class="mt-3 text-[12.5px]">{q.prompt}{#if !q.required}<span class="bn-dim"> (optional)</span>{/if}</p>
								{#if q.helpText}<p class="bn-dim text-[11px]">{q.helpText}</p>{/if}
								<div class="mt-1 flex flex-wrap gap-2">
									{#each q.options as o (o.id)}
										<Pressable
											tone={(picked[it.id]?.[q.id] ?? []).includes(o.id) ? 'primary' : 'secondary'}
											aria-pressed={(picked[it.id]?.[q.id] ?? []).includes(o.id)}
											onclick={() => toggle(it, qi, o.id)}>{o.label}</Pressable
										>
									{/each}
								</div>
								{@const freeTextPicked = q.options.some((o) => o.freeText && (picked[it.id]?.[q.id] ?? []).includes(o.id))}
								{#if freeTextPicked || q.allowOther}
									<textarea
										value={texts[it.id]?.[q.id] ?? ''}
										oninput={(e) => {
											texts[it.id] ??= {};
											texts[it.id][q.id] = (e.currentTarget as HTMLTextAreaElement).value;
										}}
										rows="2"
										placeholder={freeTextPicked ? 'Your answer' : 'Other… (type your own answer)'}
										aria-label={freeTextPicked ? 'Your answer' : 'Other answer'}
										class="pc-input mt-2 w-full resize-y rounded-[6px] px-2.5 py-2 text-[12px]"
									></textarea>
								{/if}
							{/each}
							{@const answers = answersOf(it.questions, picked[it.id] ?? {}, texts[it.id] ?? {})}
							<div class="mt-3">
								<Pressable tone="primary" disabled={busy[it.id] || !answers} onclick={() => answers && respond(it, answers)}>Send answer</Pressable>
							</div>
						{:else if it.kind === 'request_item_verdicts'}
							{#each it.items as item (item.id)}
								<div class="pc-panel mt-3 rounded-[10px] p-2.5">
									<p class="text-[12.5px] font-semibold">{item.label}</p>
									{#if item.description}<p class="bn-dim text-[11px]">{item.description}</p>{/if}
									{#if item.preview}<div class="mt-1"><Markdown text={item.preview} /></div>{/if}
									{#if item.resolved}
										<p class="mt-2 font-mono text-[11px]">
											Already decided: <Badge tone={item.resolved === 'approve' ? 'ok' : item.resolved === 'reject' ? 'err' : 'default'}
												>{VERDICT_LABEL[item.resolved] ?? item.resolved}</Badge
											>{#if item.resolvedReason}<span class="bn-dim"> · {item.resolvedReason}</span>{/if}
										</p>
									{:else}
									<div class="mt-2 flex flex-wrap gap-2">
										{#each it.verdicts as v (v)}
											<Pressable
												tone={verdictPicks[it.id]?.[item.id] === v ? 'primary' : 'secondary'}
												aria-pressed={verdictPicks[it.id]?.[item.id] === v}
												onclick={() => {
													verdictPicks[it.id] ??= {};
													verdictPicks[it.id][item.id] = v;
												}}>{VERDICT_LABEL[v] ?? v}</Pressable
											>
										{/each}
									</div>
									{#if verdictPicks[it.id]?.[item.id] && it.requireReasonOn.includes(verdictPicks[it.id][item.id])}
										<textarea
											value={verdictReasons[it.id]?.[item.id] ?? ''}
											oninput={(e) => {
												verdictReasons[it.id] ??= {};
												verdictReasons[it.id][item.id] = (e.currentTarget as HTMLTextAreaElement).value;
											}}
											rows="2"
											placeholder="Why?"
											aria-label="Reason"
											class="pc-input mt-2 w-full resize-y rounded-[6px] px-2.5 py-2 text-[12px]"
										></textarea>
									{/if}
									{/if}
								</div>
							{/each}
							{@const verdicts = verdictsOf(it, verdictPicks[it.id] ?? {}, verdictReasons[it.id] ?? {})}
							<div class="mt-3">
								<Pressable tone="primary" disabled={busy[it.id] || !verdicts || verdicts.length === 0} onclick={() => verdicts && submitVerdicts(it, verdicts)}
									>Send verdicts</Pressable
								>
							</div>
						{:else}
							{#if it.kind === 'suggest_tasks'}
								<p class="bn-dim mt-3 font-mono text-[10.5px]">Tick the tasks to create:</p>
								{#each it.tasks as task (task.clientKey)}
									<label class="mt-1 flex items-start gap-2 text-[12.5px]">
										<input
											type="checkbox"
											checked={(selections[it.id] ?? []).includes(task.clientKey)}
											onchange={() => (selections[it.id] = toggleIn(selections[it.id] ?? [], task.clientKey))}
										/>
										<span>{task.title}{#if task.description}<span class="bn-dim"> · {task.description}</span>{/if}</span>
									</label>
								{/each}
							{:else if it.kind === 'request_checkbox_confirmation'}
								{#each it.checkboxes as box (box.id)}
									<label class="mt-1 flex items-start gap-2 text-[12.5px]">
										<input type="checkbox" checked={(selections[it.id] ?? []).includes(box.id)} onchange={() => (selections[it.id] = toggleIn(selections[it.id] ?? [], box.id))} />
										<span>{box.label}{#if box.description}<span class="bn-dim"> · {box.description}</span>{/if}</span>
									</label>
								{/each}
							{/if}
							<textarea
								bind:value={reasons[it.id]}
								rows="2"
								placeholder={it.rejectRequiresReason ? `Why? (needed for "${labels.reject}")` : 'Optional note if sending back'}
								aria-label="Reason"
								class="pc-input mt-3 w-full resize-y rounded-[6px] px-2.5 py-2 text-[12px]"
							></textarea>
							<div class="mt-2 flex flex-wrap items-center gap-2">
								<Pressable
									tone="primary"
									disabled={busy[it.id] ||
										(it.kind === 'suggest_tasks' && (selections[it.id] ?? []).length === 0) ||
										(it.kind === 'request_checkbox_confirmation' && !checkboxesValid(it, selections[it.id] ?? []))}
									onclick={() => accept(it)}>{labels.accept}</Pressable
								>
								<Pressable tone="secondary" disabled={busy[it.id] || !canReject(it, reasons[it.id] ?? '')} onclick={() => reject(it)}>{labels.reject}</Pressable>
							</div>
						{/if}

						<div class="mt-3 flex flex-wrap items-end gap-2">
							<textarea
								bind:value={instructions[it.id]}
								rows="2"
								placeholder={`Instruct the agent on ${it.issueIdentifier}…`}
								aria-label="Instruction"
								class="pc-input min-w-0 flex-1 resize-y rounded-[6px] px-2.5 py-2 text-[12px]"
							></textarea>
							<Pressable tone="secondary" disabled={busy[it.id] || !(instructions[it.id] ?? '').trim()} onclick={() => instruct(it)}>Send instruction</Pressable>
						</div>
						{#if notes[it.id]}<p class="mt-2 font-mono text-[11px]">{notes[it.id]}</p>{/if}
					</Slab>
				{/each}
			</div>
		{:else if failure}
			<Slab><p class="font-mono text-[12px]" style="color: var(--bn-err)">Approvals could not be read: {failure}</p></Slab>
		{:else}
			<Slab><p class="bn-dim animate-pulse font-mono text-[12px]">loading approvals…</p></Slab>
		{/if}
	</Rise>
</div>
