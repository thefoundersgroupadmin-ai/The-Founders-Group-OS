import {render,screen,fireEvent} from '@testing-library/svelte';
import {expect,it,vi} from 'vitest';
import AgentHarnessCard from './detail/AgentHarnessCard.svelte';
it('shows execution state and actual assigned actions separately from source connectors',async()=>{
 const onTool=vi.fn();
 render(AgentHarnessCard,{agent:{name:'Data Agent',role:'Data',model:'local',tier:'worker',instance:'OS',description:'Fixture',tools:['optimal-engine','pipeline_read'],connectorTools:['optimal-engine'],taskAgent:{id:'native',name:'Renamed Data',model:'configured-model',status:'paused',tools:['pipeline_read'],toolState:'assigned'}},task:null,parentName:null,parentAgentId:null,subAgents:[],lastRun:null,onTool});
 expect(screen.getByText('task agent · paused')).toBeTruthy();
 expect(screen.getByText('assigned OS actions (1)')).toBeTruthy();
 expect(screen.getByText('source connectors (1)')).toBeTruthy();
 await fireEvent.click(screen.getByText('Pipeline Read'));
 expect(onTool).toHaveBeenCalledWith('pipeline_read');
});
