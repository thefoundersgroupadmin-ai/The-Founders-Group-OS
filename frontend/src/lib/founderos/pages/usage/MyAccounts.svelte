<!--
  Usage → My AI accounts: the signed-in person's own Claude and Codex logins
  (every AI button they press runs on these, never the owner's) and the
  "Connect this laptop" line that reports their own laptop's usage.
  GET/PUT/POST/DELETE /api/founderos/pages/ai-accounts[...].
-->
<script lang="ts">
	import { onMount } from 'svelte';
	import { founderosFetch } from '$lib/founderos/api';
	import { Badge, Pressable, SlabCard } from '$lib/founderos/kit';
	import { laptopLine, loginLine, type AiAccounts, type CodexLogin, type LaptopLine } from './accounts';

	let a = $state<AiAccounts | null>(null);
	let failure = $state<string | null>(null);
	let note = $state<string | null>(null);
	let busy = $state(false);
	let claudeToken = $state('');
	let openaiKey = $state('');
	let showKey = $state(false);
	let line = $state<LaptopLine | null>(null);
	let copied = $state(false);

	async function load() {
		try {
			const r = await founderosFetch<AiAccounts>('/pages/ai-accounts');
			if (!r?.claude || !r?.codex || !r?.laptop) throw new Error('unexpected reply');
			r.codex.login ??= { state: 'none' };
			a = r;
			failure = null;
		} catch (e) {
			failure = e instanceof Error ? e.message : String(e);
		}
	}

	async function act(fn: () => Promise<unknown>) {
		busy = true;
		note = null;
		try {
			await fn();
		} catch (e) {
			note = e instanceof Error ? e.message : String(e);
		} finally {
			busy = false;
			void load();
		}
	}

	const saveClaude = () =>
		act(async () => {
			a = await founderosFetch<AiAccounts>('/pages/ai-accounts/claude', { method: 'PUT', json: { token: claudeToken.trim() } });
			claudeToken = '';
		});
	const saveKey = () =>
		act(async () => {
			a = await founderosFetch<AiAccounts>('/pages/ai-accounts/codex', { method: 'PUT', json: { apiKey: openaiKey.trim() } });
			openaiKey = '';
			showKey = false;
		});
	const codexSignIn = () =>
		act(async () => {
			const st = await founderosFetch<CodexLogin>('/pages/ai-accounts/codex/login', { method: 'POST', json: {} });
			if (a) a.codex.login = st;
		});
	const forget = (what: 'claude' | 'codex' | 'laptop') =>
		act(async () => {
			a = await founderosFetch<AiAccounts>(`/pages/ai-accounts/${what}`, { method: 'DELETE' });
			if (what === 'laptop') line = null;
		});
	const makeLine = () =>
		act(async () => {
			line = await founderosFetch<LaptopLine>('/pages/ai-accounts/laptop', { method: 'POST', json: {} });
			copied = false;
		});
	async function copy() {
		if (!line) return;
		try {
			await navigator.clipboard.writeText(line.command);
			copied = true;
		} catch {
			copied = false;
		}
	}

	onMount(() => {
		void load();
		// a ChatGPT sign-in finishes in another tab: keep reading while one waits
		const t = setInterval(() => {
			if (a?.codex.login.state === 'waiting' || (a?.laptop.issued && !a.laptop.connected)) void load();
		}, 3000);
		return () => clearInterval(t);
	});
</script>

<SlabCard i={0} title="My AI accounts" sub="your AI buttons spend your own plans">
	<div class="space-y-4 px-6 pb-6 pt-3 text-[12.5px]">
		{#if !a}
			<p class="bn-dim font-mono text-[12px]">{failure ? `could not read your accounts: ${failure}` : 'reading…'}</p>
		{:else}
			{#if a.owner}
				<p class="bn-dim font-mono text-[11px]">You are the OS owner: your AI buttons and the scheduled agents run on this Mac's own Claude and Codex logins.</p>
			{/if}

			<!-- Claude -->
			<div class="bn-border space-y-2 border-b pb-4" data-part="claude">
				<div class="flex flex-wrap items-center justify-between gap-2">
					<span class="font-semibold">Claude</span>
					<Badge tone={a.claude.connected ? 'ok' : 'warn'}>{a.claude.connected ? 'connected' : 'not connected'}</Badge>
				</div>
				<p class="bn-dim font-mono text-[11px]">{loginLine(a.claude)}</p>
				{#if !a.owner}
					{#if !a.claude.connected}
						<ol class="bn-muted list-decimal space-y-1 pl-5 text-[12px]">
							<li>On your own laptop, open Terminal and run <code class="font-mono">claude setup-token</code> (needs Claude Code installed).</li>
							<li>Sign in with your Claude account in the browser window it opens.</li>
							<li>Paste the line it prints (starts <code class="font-mono">sk-ant-oat</code>) here.</li>
						</ol>
						<div class="flex flex-wrap gap-2">
							<input type="password" autocomplete="off" bind:value={claudeToken} placeholder="sk-ant-oat01-…" aria-label="Claude token"
								class="pc-input min-w-0 flex-1 rounded-[6px] px-2.5 py-1.5 font-mono text-[12px]" />
							<Pressable tone="primary" disabled={busy || claudeToken.trim().length < 20} onclick={saveClaude}>Save</Pressable>
						</div>
					{:else}
						<Pressable tone="secondary" disabled={busy} onclick={() => forget('claude')}>Disconnect</Pressable>
					{/if}
				{/if}
			</div>

			<!-- Codex -->
			<div class="bn-border space-y-2 border-b pb-4" data-part="codex">
				<div class="flex flex-wrap items-center justify-between gap-2">
					<span class="font-semibold">ChatGPT · Codex</span>
					<Badge tone={a.codex.connected ? 'ok' : 'warn'}>{a.codex.connected ? 'connected' : 'not connected'}</Badge>
				</div>
				<p class="bn-dim font-mono text-[11px]">{loginLine(a.codex)}</p>
				{#if !a.owner}
					{#if a.codex.login.state === 'waiting' && a.codex.login.code}
						<div class="bn-border space-y-1 rounded-[8px] border p-3">
							<p>1. Open <a class="underline" href={a.codex.login.link} target="_blank" rel="noreferrer">{a.codex.login.link}</a> and sign in with your ChatGPT account.</p>
							<p>2. Enter this code: <span class="font-mono text-[15px] font-bold tracking-[0.12em]">{a.codex.login.code}</span></p>
							<p class="bn-dim font-mono text-[10.5px]">Expires in 15 minutes. This page updates by itself when you are done.</p>
						</div>
					{:else if !a.codex.connected}
						{#if a.codex.login.state === 'failed'}
							<p class="font-mono text-[11px]" style="color: var(--bn-err)">{a.codex.login.error}</p>
						{/if}
						<div class="flex flex-wrap gap-2">
							<Pressable tone="primary" disabled={busy} onclick={codexSignIn}>{busy ? 'Starting…' : 'Sign in with ChatGPT'}</Pressable>
							<Pressable tone="secondary" disabled={busy} onclick={() => (showKey = !showKey)}>Use an API key instead</Pressable>
						</div>
						{#if showKey}
							<div class="flex flex-wrap gap-2">
								<input type="password" autocomplete="off" bind:value={openaiKey} placeholder="sk-…" aria-label="OpenAI API key"
									class="pc-input min-w-0 flex-1 rounded-[6px] px-2.5 py-1.5 font-mono text-[12px]" />
								<Pressable tone="primary" disabled={busy || openaiKey.trim().length < 20} onclick={saveKey}>Save</Pressable>
							</div>
						{/if}
					{:else}
						<Pressable tone="secondary" disabled={busy} onclick={() => forget('codex')}>Disconnect</Pressable>
					{/if}
				{/if}
			</div>

			<!-- Laptop -->
			<div class="space-y-2" data-part="laptop">
				<div class="flex flex-wrap items-center justify-between gap-2">
					<span class="font-semibold">This laptop's usage</span>
					<Badge tone={a.laptop.connected ? 'ok' : 'default'}>{a.laptop.connected ? 'reporting' : 'not connected'}</Badge>
				</div>
				<p class="bn-dim font-mono text-[11px]">{laptopLine(a.laptop)}</p>
				<p class="bn-muted text-[12px]">Shows the Claude and Codex work you do on your own laptop on this page. It reads only usage numbers: no chats, files or notes.</p>
				{#if line}
					<p class="text-[12px]">On your Mac, open <b>Terminal</b>, paste this line and press Enter:</p>
					<div class="flex flex-wrap items-start gap-2">
						<code data-part="laptop-line" class="bn-border min-w-0 flex-1 break-all rounded-[6px] border bg-[var(--bn-bg)] p-2 font-mono text-[11px]">{line.command}</code>
						<Pressable tone="primary" onclick={copy}>{copied ? 'Copied' : 'Copy'}</Pressable>
					</div>
					<p class="bn-dim font-mono text-[10.5px]">The line is yours only. Don't share it; making a new one stops the old one.</p>
				{:else}
					<div class="flex flex-wrap gap-2">
						<Pressable tone="primary" disabled={busy} onclick={makeLine}>{a.laptop.issued ? 'Make a new install line' : 'Connect this laptop'}</Pressable>
						{#if a.laptop.issued}
							<Pressable tone="secondary" disabled={busy} onclick={() => forget('laptop')}>Disconnect laptop</Pressable>
						{/if}
					</div>
				{/if}
			</div>

			{#if note}<p class="font-mono text-[11px]" style="color: var(--bn-err)">{note}</p>{/if}
		{/if}
	</div>
</SlabCard>
