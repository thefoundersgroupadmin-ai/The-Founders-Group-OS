<script lang="ts">
 import { onMount } from 'svelte';
 import { founderosFetch } from '$lib/founderos/api';
 import { SlabCard, PILL } from '$lib/founderos/kit';
 import SkillMarkdown from './SkillMarkdown.svelte';
 type Workspace={workspaceId:string;workspace:string;name:string;profile:string;enabled:boolean;timezone:string;sourceWorkspaceIds?:string[]};
 type Run={id:string;routines:string[];skills:string[];status:string;markdown:string;error:string;startedAt:string;sourceIds:string[]};
 type State={configured:boolean;detail?:string;workspaces:Workspace[];selected?:Workspace;canConfigure?:boolean;schedulesEnabled?:boolean;paused?:boolean;hasSources?:boolean;gaps?:string[];runs:Run[]};
 let data=$state<State|null>(null);
 let workspaceId=$state('');
 let profile=$state('');
 let timezone=$state('Europe/London');
 let enabled=$state(false);
 let sourceWorkspaceIds=$state<string[]>([]);
 let failure=$state('');
 let notice=$state('');
 let busy=$state(false);
 let editing=$state(false);
 let selectedRun=$state('');
 let sequence=0;
 const run=$derived(data?.runs.find(r=>r.id===selectedRun)??data?.runs[0]);
 const status=$derived(!data?'Loading automation…':!data.configured?'Not configured':data.paused?'Paused at usage cutoff':!data.schedulesEnabled?'OS schedules are off':!data.selected?.enabled?'Setup needed':!data.hasSources?'Waiting for synced sales records':'Automatic');
 async function load(reset=false){
  const ticket=++sequence;
  try{
   const state=await founderosFetch<State>(`/pages/sales-kit/automation${workspaceId?`?workspace=${encodeURIComponent(workspaceId)}`:''}`);
   if(ticket!==sequence)return;
   data=state;failure='';
   if(reset||!editing){workspaceId=state.selected?.workspaceId??'';profile=state.selected?.profile??'';timezone=state.selected?.timezone??'Europe/London';enabled=state.selected?.enabled??false;sourceWorkspaceIds=state.selected?.sourceWorkspaceIds??[];}
  }catch(e){if(ticket===sequence)failure=e instanceof Error?e.message:'Automation unavailable';}
 }
 onMount(()=>{
  void load(true);
  const timer=setInterval(()=>{if(!busy&&!editing)void load();},15000);
  return ()=>{clearInterval(timer);sequence++;};
 });
 async function save(){
  busy=true;failure='';notice='';
  try{
   await founderosFetch('/pages/sales-kit/automation',{method:'PUT',json:{workspaceId,profile,timezone,enabled,sourceWorkspaceIds}});
   editing=false;await load(true);
   notice=enabled?'Automation saved. Relevant work will run when sales records change and during daily and weekly reviews.':'Automation paused for this workspace.';
  }catch(e){failure=e instanceof Error?e.message:'Could not save automation';}finally{busy=false;}
 }
</script>

<SlabCard i={1} title="Sales automation" sub={status} class="mt-6">
 <div class="space-y-5 px-6 py-5 text-[12px]">
  <p class="bn-dim">Configure each workspace once. The OS watches its synced leads and archived calls, selects the relevant workflows, and saves one consolidated report. No separate execution for each skill.</p>
  {#if failure}<p role="alert" style="color:var(--bn-err)">{failure}</p>{/if}
  {#if notice}<p role="status">{notice}</p>{/if}
  {#if data?.configured}
   <label class="flex max-w-lg flex-col gap-2">Sales workspace
    <select bind:value={workspaceId} disabled={busy} onchange={()=>{editing=false;selectedRun='';void load(true);}} class="bn-border border bg-[var(--bn-bg)] p-2">
     {#each data.workspaces as ws}<option value={ws.workspaceId}>{ws.name}{ws.enabled?' · automatic':''}</option>{/each}
    </select>
   </label>
   <div class="grid gap-3 sm:grid-cols-2">
    <div class="border p-3 bn-border"><strong>Lead changes</strong><p class="bn-dim mt-1">Qualification, prospect research, outreach drafts and SDR handoff.</p></div>
    <div class="border p-3 bn-border"><strong>New call records</strong><p class="bn-dim mt-1">Call summary, customer follow-up draft and activity log draft.</p></div>
    <div class="border p-3 bn-border"><strong>Every day</strong><p class="bn-dim mt-1">Sales briefing, pipeline review and CRM hygiene.</p></div>
    <div class="border p-3 bn-border"><strong>Every week</strong><p class="bn-dim mt-1">Sales review and win/loss analysis.</p></div>
   </div>
   {#if data.paused}<p style="color:var(--bn-warn)">The usage pause blocks automatic AI work until the owner explicitly resumes it.</p>{/if}
   {#if !data.schedulesEnabled}<p style="color:var(--bn-warn)">Scheduled work is disabled by the OS. This workspace will not run until OS schedules are enabled.</p>{/if}
   {#if data.canConfigure && data.selected}
    <details open={!data.selected.enabled}>
     <summary class="cursor-pointer py-2">Workspace setup and automation controls</summary>
     <fieldset disabled={busy} class="space-y-4 py-3">
      <label class="flex flex-col gap-2">Approved sales profile
       <textarea bind:value={profile} oninput={()=>editing=true} rows="5" maxlength="24000" placeholder="Company, offers, ideal customer, qualification criteria, sales stages, tone and contact rules. Saved once for this workspace." class="bn-border w-full border bg-[var(--bn-bg)] p-3"></textarea>
      </label>
      <label class="flex max-w-lg flex-col gap-2">Review timezone
       <input bind:value={timezone} oninput={()=>editing=true} placeholder="Europe/London" class="bn-border border bg-[var(--bn-bg)] p-2" />
      </label>
      <fieldset class="space-y-2">
       <legend class="mb-2">Connected source workspaces</legend>
       <p class="bn-dim">This workspace's records are always included. Select only workspaces belonging to this sales operation.</p>
       {#each data.workspaces.filter(w=>w.workspaceId!==workspaceId) as ws}
        <label class="flex items-center gap-2"><input type="checkbox" value={ws.workspaceId} bind:group={sourceWorkspaceIds} onchange={()=>editing=true}/>{ws.name}</label>
       {/each}
      </fieldset>
      <label class="flex items-center gap-2"><input type="checkbox" bind:checked={enabled} onchange={()=>editing=true} /> Automatically analyse this workspace</label>
      <button type="button" class={PILL} disabled={!profile.trim()} onclick={save}>{busy?'Saving…':'Save automation settings'}</button>
     </fieldset>
    </details>
   {/if}
   {#if data.gaps?.length}<details><summary class="bn-dim cursor-pointer">Source coverage and gaps</summary><ul class="bn-dim mt-2 list-disc space-y-1 pl-5">{#each data.gaps as gap}<li>{gap}</li>{/each}</ul></details>{/if}
   <p class="bn-dim">Checks every minute while the OS is running. Unchanged records are skipped. Analysis and drafts are saved here; messages, bookings and CRM changes are not applied automatically.</p>
   <div class="space-y-3 border-t pt-4 bn-border">
    <h3 class="font-bold">Automatic work history</h3>
    {#if data.runs.length===0}<p class="bn-dim">No runs yet. Enable the workspace after setting its sales profile; the first run needs synced sales records.</p>
    {:else}
     <label class="flex flex-col gap-2">Saved report
      <select bind:value={selectedRun} class="bn-border border bg-[var(--bn-bg)] p-2"><option value="">Latest report</option>{#each data.runs as item}<option value={item.id}>{new Date(item.startedAt).toLocaleString()} · {item.status} · {item.routines.join(', ')}</option>{/each}</select>
     </label>
     {#if run}<p class="bn-dim">{run.status} · {run.skills.length} workflows · sources: {run.sourceIds.join(', ')}</p>{#if run.error}<p role="alert" style="color:var(--bn-warn)">{run.error}</p>{/if}{#if run.markdown}<SkillMarkdown src={run.markdown}/>{:else if run.status==='running'}<p class="bn-dim">Analysis in progress. The report will appear here automatically.</p>{/if}{/if}
    {/if}
   </div>
  {:else if data}<p class="bn-dim">{data.detail??'Sales automation is not configured.'}</p>{/if}
 </div>
</SlabCard>
