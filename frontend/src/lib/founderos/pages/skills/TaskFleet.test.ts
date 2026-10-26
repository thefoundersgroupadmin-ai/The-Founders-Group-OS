import {render,screen,fireEvent,cleanup} from '@testing-library/svelte';
import {afterEach,it,expect,vi} from 'vitest';
import TaskFleet from './TaskFleet.svelte';
const api=vi.hoisted(()=>({request:vi.fn()}));
vi.mock('$lib/founderos/api',()=>({founderosFetch:api.request}));
afterEach(()=>{cleanup();vi.resetAllMocks();});
it('separates role coverage, availability, running work and missing credentials',async()=>{
 api.request.mockResolvedValue({workers:35,covered:35,taskAgents:39,available:39,running:0,roles:[{workerId:'payments-pulse',name:'Payments Pulse',provisioned:true,taskAgent:{id:'a',status:'idle'},connections:[{id:'payments',state:'not_configured',missingKeys:['STRIPE_SECRET_KEY']}]}]});
 render(TaskFleet);
 await screen.findByText(/35 \/ 35 worker roles/);
 expect(screen.getByText(/39 total task agents/).textContent).toContain('0 running');
 await fireEvent.click(screen.getByText('Roles and connection requirements'));
 expect(screen.getByText(/Missing: STRIPE_SECRET_KEY/)).toBeTruthy();
 expect(screen.queryByRole('button',{name:'Set up missing task agents'})).toBeNull();
});
it('provisions only through the OS action and refreshes actual coverage',async()=>{
 let ready=false;
 api.request.mockImplementation((path:string)=>{if(path.endsWith('/provision')){ready=true;return Promise.resolve({created:1,existing:34,results:[]});}return Promise.resolve({workers:35,covered:ready?35:34,taskAgents:ready?39:38,available:38,running:0,roles:[]});});
 render(TaskFleet);await fireEvent.click(await screen.findByRole('button',{name:'Set up missing task agents'}));
 await screen.findByText(/35 \/ 35 worker roles/);
 expect(api.request).toHaveBeenCalledWith('/pages/agents/fleet/provision',{method:'POST',json:{}});
});
