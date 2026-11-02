<script lang="ts">
 import { onMount } from 'svelte';
 import { founderosFetch, founderosUrl } from '$lib/founderos/api';
 import { SlabCard, PILL } from '$lib/founderos/kit';
 import SkillMarkdown from './SkillMarkdown.svelte';
 type Skill = { id:string; name:string; category:string; method:string; output:string };
 type Catalog = { version:string; skills:Skill[] };
 let catalog=$state<Catalog|null>(null);
 let failure=$state('');
 let selected=$state('lead-triage');
 let workspace=$state('');
 let profile=$state('');
 let request=$state('');
 let sources=$state([{id:'source-1',title:'',text:''}]);
 let nextSource=2;
 let busy=$state(false);
 let output=$state('');
 let outputKind=$state<'prompt'|'draft'>('draft');
 let outputSkill=$state('');
 const skill=$derived(catalog?.skills.find(s=>s.id===selected));
 const categories=$derived([...new Set(catalog?.skills.map(s=>s.category)??[])]);
 const ready=$derived(workspace.trim()!=='' && profile.trim()!=='' && request.trim()!=='' && sources.every(s=>s.text.trim()!==''));
 onMount(()=>{founderosFetch<Catalog>('/pages/sales-kit').then(v=>{if(!v || !Array.isArray(v.skills)) throw new Error('Sales Kit catalog unavailable');catalog=v;}).catch(e=>failure=e instanceof Error?e.message:'Sales Kit unavailable');});
 async function run(generate:boolean){
  busy=true; failure=''; output='';
  try{
   const result=await founderosFetch<{prompt?:string;markdown?:string}>(`/pages/sales-kit/${generate?'run':'prepare'}/${selected}`,{method:'POST',json:{workspace,profile,request,sources}});
   output=result.markdown??result.prompt??''; outputKind=generate?'draft':'prompt'; outputSkill=selected;
  }catch(e){failure=e instanceof Error?e.message:'Could not prepare this workflow';}finally{busy=false;}
 }
 function download(){
  const blob=new Blob([output],{type:'text/markdown;charset=utf-8'});
  const url=URL.createObjectURL(blob);const a=document.createElement('a');a.href=url;a.download=`sales-${outputSkill}-${outputKind}.md`;a.click();URL.revokeObjectURL(url);
 }
</script>

<SlabCard i={6} title="Sales Kit" sub={catalog?`${catalog.skills.length} workflows · v${catalog.version}`:'sales analysis and drafts'} class="mt-6">
 {#snippet action()}
  <a class={PILL} href={founderosUrl('/pages/sales-kit/export')}>Download client package</a>
 {/snippet}
 <div class="space-y-5 px-6 py-5 text-[12px]">
  <p class="bn-dim">Research, qualify, prepare meetings and review your pipeline. Add your client profile and relevant records to produce an analysis or draft for review.</p>
  {#if failure}<p role="alert" style="color:var(--bn-err)">{failure}</p>{/if}
  {#if catalog}
   <fieldset disabled={busy} class="space-y-4 disabled:opacity-60">
    <div class="grid gap-4 md:grid-cols-2">
     <label class="flex flex-col gap-2">Workflow
      <select bind:value={selected} class="bn-border border bg-[var(--bn-bg)] p-2">
       {#each categories as category}<optgroup label={category}>{#each catalog.skills.filter(s=>s.category===category) as item}<option value={item.id}>{item.name}</option>{/each}</optgroup>{/each}
      </select>
     </label>
     <label class="flex flex-col gap-2">Client workspace label
      <input bind:value={workspace} maxlength="100" placeholder="e.g. Acme — New Business" class="bn-border border bg-[var(--bn-bg)] p-2" />
     </label>
    </div>
    {#if skill}<p class="bn-dim">{skill.method}<br /><span class="bn-text">Result: {skill.output}</span></p>{/if}
    <label class="flex flex-col gap-2">Client profile
     <textarea bind:value={profile} rows="4" maxlength="24000" placeholder="Company, offer, ideal customer, qualification criteria, sales stages, tone and contact rules." class="bn-border w-full border bg-[var(--bn-bg)] p-3"></textarea>
    </label>
    <label class="flex flex-col gap-2">What do you need?
     <textarea bind:value={request} rows="2" maxlength="8000" placeholder="e.g. Qualify this enquiry and recommend the next question." class="bn-border w-full border bg-[var(--bn-bg)] p-3"></textarea>
    </label>
    <p class="bn-dim">Sources are supplied for this run. Add relevant CRM records, emails, transcripts or calendar availability with dates. The runner does not fetch connected accounts. Keep credentials out of the source text.</p>
    {#each sources as source,index (source.id)}
     <div class="space-y-2 border p-3 bn-border">
      <div class="flex items-center justify-between gap-3"><span>Source {index+1} · [{source.id}]</span>{#if sources.length>1}<button type="button" class={PILL} onclick={()=>sources=sources.filter(s=>s.id!==source.id)}>Remove source {index+1}</button>{/if}</div>
      <label class="flex flex-col gap-2">Source {index+1} title
       <input bind:value={source.title} maxlength="300" placeholder="e.g. CRM lead 123 — updated today" class="bn-border w-full border bg-[var(--bn-bg)] p-2" />
      </label>
      <label class="flex flex-col gap-2">Source {index+1} records
       <textarea bind:value={source.text} rows="5" maxlength="120000" placeholder="Paste relevant records here, including IDs, dates and opt-out status." class="bn-border w-full border bg-[var(--bn-bg)] p-3"></textarea>
      </label>
     </div>
    {/each}
    <button type="button" class={PILL} disabled={sources.length>=30} onclick={()=>sources=[...sources,{id:`source-${nextSource++}`,title:'',text:''}]}>Add source</button>
   </fieldset>
   <div class="flex flex-wrap items-center gap-3">
    <button type="button" class={PILL} disabled={!ready||busy} onclick={()=>run(false)}>{busy?'Working…':'Prepare prompt'}</button>
    <button type="button" class={PILL} disabled={!ready||busy} onclick={()=>run(true)}>Generate analysis / draft</button>
    <a class={PILL} href={founderosUrl(`/pages/sales-kit/skills/${selected}`)}>Download workflow</a>
   </div>
   <p class="bn-dim">Prepare prompt uses no AI. Generate uses your Claude login and checks the OS usage pause. Results stay in this view until downloaded; a download is saved to your device. Review before sharing or applying changes.</p>
  {/if}
  {#if output}
   <div class="space-y-3 border-t pt-4 bn-border" aria-live="polite">
    <div class="flex items-center justify-between"><h3>{outputKind==='draft'?'Analysis / draft for review':'Prepared prompt'}</h3><button type="button" class={PILL} onclick={download}>Download {outputKind}</button></div>
    <p class="bn-dim">Generated from supplied records. Review the cited evidence before use.</p>
    {#if outputKind==='prompt'}<pre class="max-h-[32rem] overflow-auto whitespace-pre-wrap text-[11px]">{output}</pre>{:else}<SkillMarkdown src={output}/>{/if}
   </div>
  {/if}
 </div>
</SlabCard>
