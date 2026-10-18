<!-- Shown in place of the /os views when the signed-in account doesn't own a
     Founder OS yet (fresh install, or a teammate's account): one panel with
     the one command that sets it up, instead of every card failing. -->
<script lang="ts">
	import { PageHeader } from '$lib/founderos/kit';
	import { COMPANY_SHORT, WORKSPACE_NAMES } from '$lib/founderos/brand.gen';

	let { email }: { email: string } = $props();
	const command = $derived(`make tfg OWNER=${email || '<your email>'}`);
</script>

<section class="bn-card mx-auto mt-10 max-w-2xl p-6" data-part="setup-notice">
	<PageHeader eyebrow="first run" title={`Set up ${COMPANY_SHORT} OS`} />
	<p class="bn-muted mt-4 font-mono text-[12px] leading-relaxed">
		This account doesn't have a {COMPANY_SHORT} OS yet. From the repository root, create its workspaces
		({Object.values(WORKSPACE_NAMES).join(', ')}) and load the {COMPANY_SHORT} starting data:
	</p>
	<pre class="setup-cmd bn-text mt-4 overflow-x-auto px-4 py-3 font-mono text-[12px]">{command}</pre>
	<p class="bn-dim mt-4 font-mono text-[11px] leading-relaxed">Then reload this page. Re-running it is safe; it only refreshes the starting data.</p>
</section>

<style>
	.setup-cmd {
		background: var(--bn-surface-2);
		border: 1px solid var(--bn-border);
		border-radius: var(--bn-r-chip);
	}
</style>
