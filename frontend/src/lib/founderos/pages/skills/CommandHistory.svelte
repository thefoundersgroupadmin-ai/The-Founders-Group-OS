<script lang="ts">
 import { onMount } from 'svelte';
 import { currentWorkspace } from '$lib/stores/workspaces';
 import { page } from '$app/stores';
 import { founderosFetch } from '$lib/founderos/api';
 import { SlabCard, PILL } from '$lib/founderos/kit';
 import SkillMarkdown from './SkillMarkdown.svelte';
 type Command={id:string;workspaceId:string;request:string;status:string;report:string;steps:{kind:string;id:string;status:string;output?:string;taskId?:string}[];createdAt:string};
 let commands=$state<Command[]>([]);
 let message=$state('');
 let busy=$state(false);
 let failure=$state('');
 let receipt=$state('');
 let alive=true;
 async function load(){try{const d=await founderosFetch<{commands:Command[]}>('/pages/conductor/commands');if(alive)commands=d.commands??[];}catch(e){if(alive)failure=e instanceof Error?e.message:'Command history unavailable';}}
 onMount(()=>{alive=true;void load();const timer=setInterval(()=>void load(),10000);return()=>{alive=false;clearInterval(timer);};});
 async function submit(){
  if(!message.trim()||busy)return;
  busy=true;failure='';receipt='';
  const requestId=crypto.randomUUID();
  try{
   const d=await founderosFetch<{reply?:{content:string;receipt?:{text:string}}}>('/pages/conductor/ask',{method:'POST',json:{message:message.trim(),workspaceId:$currentWorkspace?.id,requestId,path:'/os/skills'}});
   receipt=[d.reply?.content,d.reply?.receipt?.text].filter(Boolean).join(' ');message='';await load();
  }catch(e){failure=e instanceof Error?e.message:'Command could not start';}finally{busy=false;}
 }
</script>

<SlabCard i={1} title="Give the OS a command" sub="Agents, workflows and results in one place" class="mt-6">
 <div class="space-y-4 px-6 py-5 text-[12px]">
  <p class="bn-dim">Describe the outcome. The OS selects the relevant workers, passes your workspace profile and workflows to the team, and records each step. You can also speak or type to the Conductor from any screen.</p>
  <form onsubmit={(e)=>{e.preventDefault();void submit();}} class="space-y-3">
   <label class="flex flex-col gap-2">Command
    <textarea bind:value={message} maxlength="8000" rows="2" placeholder="Run my sales system: review the pipeline, prioritise buyers and prepare the next outreach." class="bn-border w-full border bg-[var(--bn-bg)] p-3"></textarea>
   </label>
   <button class={PILL} type="submit" disabled={busy||!message.trim()}>{busy?'Starting…':'Start command'}</button>
  </form>
  {#if failure}<p role="alert" style="color:var(--bn-err)">{failure}</p>{/if}
  {#if receipt}<p role="status">{receipt}</p>{/if}
  <p class="bn-dim">Messages awaiting approval appear in <a href="/os/outbox" class="underline">Outbox</a>. Agent questions appear in <a href="/os/approvals" class="underline">Approvals</a>. Missing connections can be configured in <a href="/os/integrations" class="underline">Connections</a>.</p>
  {#if commands.length===0}<p class="bn-dim">Your commands and progress will appear here.</p>{/if}
  {#each commands as command (command.id)}
   <details id={`command-${command.id}`} class="bn-border border p-3" open={$page?.url?.searchParams?.get('command')===command.id||command.status==='running'||command.status==='waiting'||command.status==='queued'}>
    <summary class="cursor-pointer">{command.status} · {command.request} · {new Date(command.createdAt).toLocaleString()}</summary>
    <ol class="my-3 space-y-2 pl-4 list-decimal">{#each command.steps as step}<li>{step.kind} · {step.id} · {step.status}{#if step.taskId} · <a href="/os/tasks" class="underline">Task</a>{/if}</li>{/each}</ol>
    {#if command.report}<SkillMarkdown src={command.report}/>{/if}
   </details>
  {/each}
 </div>
</SlabCard>
