<script lang="ts">
 import {onMount} from 'svelte';
 import {founderosFetch} from '$lib/founderos/api';
 import {Slab,Pressable} from '$lib/founderos/kit';
 type Action={id:string;tool:string;status:string;input:unknown;output:unknown;taskId:string};
 let actions=$state<Action[]>([]);let error=$state('');let busy=$state('');let alive=true;
 async function load(){try{const r=await founderosFetch<{requests:Action[]}>('/pages/agent-tools');if(alive)actions=r.requests??[];}catch(e){if(alive)error=e instanceof Error?e.message:'Tool requests unavailable';}}
 onMount(()=>{alive=true;void load();const timer=setInterval(()=>void load(),15000);return()=>{alive=false;clearInterval(timer);};});
 async function decide(id:string,decision:'approve'|'reject'){busy=id;error='';try{const result=await founderosFetch<{deliveryError?:string}>(`/pages/agent-tools/${encodeURIComponent(id)}/${decision}`,{method:'POST',json:{}});await load();if(result.deliveryError)error=`Decision saved. Task notification failed: ${result.deliveryError}. Do not repeat the action.`;}catch(e){error=e instanceof Error?e.message:'Decision failed';}finally{busy='';}}
</script>
<Slab>
 <h2 class="mb-3 font-mono text-[14px]">Agent tool actions</h2>
 <p class="bn-dim mb-3 text-[12px]">Review the exact recipients, content and generation settings. Approval executes this action once. Unknown outcomes require checking the provider before another attempt.</p>
 {#if error}<p role="alert" style="color:var(--bn-err)">{error}</p>{/if}
 {#if !actions.length}<p class="bn-dim text-[12px]">No tool action requests.</p>{/if}
 {#each actions as action (action.id)}
  <details class="bn-border mb-3 border p-3" open={action.status==='needs_approval'}>
   <summary>{action.tool} · {action.status}</summary>
   <pre class="my-3 whitespace-pre-wrap break-words text-[11px]">{JSON.stringify(action.input,null,2)}</pre>
   {#if action.status==='needs_approval'}
    <Pressable tone="primary" disabled={!!busy} onclick={()=>void decide(action.id,'approve')}>Approve this action</Pressable>
    <Pressable tone="secondary" disabled={!!busy} onclick={()=>void decide(action.id,'reject')}>Reject</Pressable>
   {:else}<pre class="whitespace-pre-wrap break-words text-[11px]">{JSON.stringify(action.output,null,2)}</pre>{/if}
  </details>
 {/each}
</Slab>
