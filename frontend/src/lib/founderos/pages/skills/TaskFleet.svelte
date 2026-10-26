<script lang="ts">
 import { onMount } from 'svelte';
 import { founderosFetch } from '$lib/founderos/api';
 import { SlabCard, PILL } from '$lib/founderos/kit';
 type Role={workerId:string;name:string;description:string;provisioned:boolean;taskAgent?:{id:string;status:string};connections:{id:string;state:string;missingKeys:string[]}[]};
 type Fleet={workers:number;covered:number;taskAgents:number;available:number;running:number;roles:Role[]};
 let fleet=$state<Fleet|null>(null);
 let error=$state('');let busy=$state(false);let notice=$state('');let alive=true;
 async function load(){try{const data=await founderosFetch<Fleet>('/pages/agents/fleet');if(alive){fleet=data;error='';}}catch(e){if(alive)error=e instanceof Error?e.message:'Task fleet unavailable';}}
 onMount(()=>{alive=true;void load();const timer=setInterval(()=>void load(),60000);return()=>{alive=false;clearInterval(timer);};});
 async function provision(){busy=true;notice='';error='';try{const r=await founderosFetch<{created:number;existing:number;results:{error?:string}[]}>('/pages/agents/fleet/provision',{method:'POST',json:{}});const errors=r.results.filter(x=>x.error);notice=`${r.created} task agents created; ${r.existing} existing roles preserved.`;if(errors.length)error=errors.map(x=>x.error).join('; ');await load();}catch(e){error=e instanceof Error?e.message:'Task agent setup failed';}finally{busy=false;}}
</script>
<SlabCard i={2} title="Task-agent coverage" sub="Live task agents and their linked OS workers" class="mt-6">
 <div class="space-y-4 px-6 py-5 text-[12px]">
  {#if fleet}
   <p>{fleet.covered} / {fleet.workers} worker roles have task agents · {fleet.taskAgents} total task agents · {fleet.available} available · {fleet.running} running</p>
   <p class="bn-dim">Each role owns tasks, uses the shared workflows and receives previous agents’ saved results. Agents start when work arrives. A missing connection limits the actions that role can perform.</p>
   {#if fleet.covered<fleet.workers}<button class={PILL} onclick={()=>void provision()} disabled={busy}>{busy?'Setting up roles…':'Set up missing task agents'}</button>{/if}
   <details><summary class="cursor-pointer">Roles and connection requirements</summary>
    <div class="mt-3 space-y-3">
     {#each fleet.roles as role (role.workerId)}
      <div class="bn-border border p-3">
       <p>{role.name} · {role.provisioned?(role.taskAgent?.status??'unknown'):'task agent missing'}</p>
       {#each role.connections as connection}
        <p class="bn-dim">{connection.id}: {connection.state}{#if connection.missingKeys.length} · Missing: {connection.missingKeys.join(', ')}{/if}</p>
       {/each}
      </div>
     {/each}
    </div>
   </details>
   <a href="/os/integrations" class="underline">Configure credentials in OS Connections</a>
  {:else if !error}<p class="bn-dim">Checking task agents…</p>{/if}
  {#if notice}<p role="status">{notice}</p>{/if}
  {#if error}<p role="alert" style="color:var(--bn-err)">{error}</p>{/if}
 </div>
</SlabCard>
