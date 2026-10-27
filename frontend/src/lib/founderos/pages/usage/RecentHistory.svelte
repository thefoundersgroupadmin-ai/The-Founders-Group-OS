<script lang="ts">
 import {SlabCard} from '$lib/founderos/kit';
 import {burnOf,fmtTokens,type UsageBody} from './usage';
 let {board}:{board:UsageBody}=$props();
 const plans=$derived([{name:'Claude',plan:board.claude},{name:'Codex',plan:board.codex}].filter(p=>p.plan));
 const rows=$derived(plans.flatMap(({name,plan})=>(plan!.historyDays??plan!.days.map(d=>({...d,reported:true}))).map(d=>({...d,provider:name}))).sort((a,b)=>b.day.localeCompare(a.day)||a.provider.localeCompare(b.provider)));
 const total=$derived(rows.filter(d=>d.reported).reduce((n,d)=>n+burnOf(d),0));
</script>
<SlabCard title="Last 8 days" sub="Today and the previous seven calendar days">
 <div class="px-6 pb-5">
  <p class="mb-3 text-[20px] font-semibold tabular-nums">{rows.some(d=>d.reported)?fmtTokens(total):'—'} <span class="bn-dim text-[12px]">recorded tokens burned</span></p>
  <div class="overflow-x-auto">
   <table class="w-full text-left font-mono text-[11px]" aria-label="Last 8 days of token usage">
    <thead><tr class="bn-dim"><th class="p-2">Day</th><th class="p-2">Provider</th><th class="p-2">Input</th><th class="p-2">Output</th><th class="p-2">Cache write</th><th class="p-2">Cache read</th><th class="p-2">Burn</th></tr></thead>
    <tbody>{#each rows as day (`${day.day}-${day.provider}`)}<tr class="bn-border border-t"><td class="p-2">{day.day}</td><td class="p-2">{day.provider}</td>{#each [day.in,day.out,day.cacheWrite,day.cacheRead,burnOf(day)] as n}<td class="p-2 tabular-nums" title={day.reported?n.toLocaleString('en-GB'):'Not reported'}>{day.reported?fmtTokens(n):'—'}</td>{/each}</tr>{/each}</tbody>
   </table>
  </div>
  <p class="bn-dim mt-3 text-[11px]">Burn = input + output + cache writes. Cache reads are shown separately. — means not reported; totals include recorded data only. Weekly plan gauges remain seven days.</p>
 </div>
</SlabCard>
