<!-- /os/dialer: ring prospects back to back (lib/founderos/pages/dialer). -->
<script lang="ts">
	import { Slab, SlabTitle } from '$lib/founderos/kit';
	import DialerPage from '$lib/founderos/pages/dialer/DialerPage.svelte';
</script>

<Slab>
	<SlabTitle eyebrow="outbound · one call at a time · their local hours" title="Dialer" meta="Twilio calls in the browser · Apollo numbers · outcomes move the pipeline" />
	<DialerPage />
</Slab>
