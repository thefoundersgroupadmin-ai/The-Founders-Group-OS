<!-- What is waiting on the operator, shown where he already looks (Home,
     Agents) so a request never sits unseen: agent board requests (handled in
     /os/approvals) and email waiting to send (/os/outbox). Renders nothing
     while there is nothing to do or the count cannot be read. -->
<script lang="ts">
	import { onMount } from 'svelte';
	import { founderosFetch } from '$lib/founderos/api';
	import { Slab } from '$lib/founderos/kit';
	import type { ApprovalsBody } from './types';

	let counts = $state<{ requests: number; emails: number } | null>(null);

	onMount(() => {
		founderosFetch<ApprovalsBody>('/pages/approvals')
			.then((b) => (counts = { requests: b.items.length, emails: b.outboxPending }))
			.catch(() => (counts = null));
	});
</script>

{#if counts && (counts.requests > 0 || counts.emails > 0)}
	<Slab>
		<p class="font-mono text-[12px]" data-part="approvals-notice">
			Waiting for you:
			{#if counts.requests > 0}
				<a href="/os/approvals" class="underline underline-offset-2">{counts.requests} agent request{counts.requests === 1 ? '' : 's'} in Approvals</a>
			{/if}
			{#if counts.requests > 0 && counts.emails > 0}<span class="bn-dim"> · </span>{/if}
			{#if counts.emails > 0}
				<a href="/os/outbox" class="underline underline-offset-2">{counts.emails} email{counts.emails === 1 ? '' : 's'} in the Outbox</a>
			{/if}
		</p>
	</Slab>
{/if}
