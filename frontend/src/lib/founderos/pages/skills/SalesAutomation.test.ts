import { fireEvent, render, screen, waitFor, cleanup } from '@testing-library/svelte';
import { afterEach, expect, it, vi } from 'vitest';
import SalesAutomation from './SalesAutomation.svelte';
const api=vi.hoisted(()=>({request:vi.fn()}));
vi.mock('$lib/founderos/api',()=>({founderosFetch:api.request}));
afterEach(()=>{cleanup();vi.resetAllMocks();});
const workspace={workspaceId:'ws-a',workspace:'client-a',name:'Client A',profile:'Approved inventory offer',enabled:false,timezone:'Europe/London'};
const state={configured:true,workspaces:[workspace],selected:workspace,canConfigure:true,schedulesEnabled:true,paused:false,hasSources:true,gaps:[],runs:[]};
it('configures a workspace once instead of asking to execute separate skills',async()=>{
 api.request.mockResolvedValue(state);render(SalesAutomation);
 await screen.findByLabelText('Sales workspace');
 await fireEvent.click(screen.getByLabelText('Automatically analyse this workspace'));
 await fireEvent.click(screen.getByRole('button',{name:'Save automation settings'}));
 await waitFor(()=>expect(api.request).toHaveBeenCalledWith('/pages/sales-kit/automation',expect.objectContaining({method:'PUT',json:{workspaceId:'ws-a',profile:'Approved inventory offer',timezone:'Europe/London',enabled:true}})));
 expect(screen.queryByRole('button',{name:/execute|generate|run/i})).toBeNull();
});
it('shows saved consolidated outputs and source coverage',async()=>{
 api.request.mockResolvedValue({...state,selected:{...workspace,enabled:true},runs:[{id:'run-a',routines:['lead changes','daily pipeline review'],skills:['lead-triage','draft-outreach'],status:'complete',markdown:'# Automatic report\n\nDraft outreach [os-pipeline]',error:'',startedAt:'2026-10-25T09:00:00Z',sourceIds:['os-pipeline']}]});
 render(SalesAutomation);
 expect(await screen.findByRole('heading',{name:'Automatic report'})).toBeTruthy();
 expect(screen.getByText(/2 workflows/)).toBeTruthy();
});
it('shows that the global schedule guard blocks automation',async()=>{
 api.request.mockResolvedValue({...state,schedulesEnabled:false});render(SalesAutomation);
 await screen.findByText(/Scheduled work is disabled by the OS/);
 expect(api.request.mock.calls.every(([,options])=>!options)).toBe(true);
});
