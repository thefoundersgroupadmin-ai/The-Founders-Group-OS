import {render,screen,fireEvent,cleanup} from '@testing-library/svelte';
import {afterEach,it,expect,vi} from 'vitest';
import ToolApprovals from './ToolApprovals.svelte';
const api=vi.hoisted(()=>({request:vi.fn()}));
vi.mock('$lib/founderos/api',()=>({founderosFetch:api.request}));
afterEach(()=>{cleanup();vi.resetAllMocks();});
it('shows the exact payload and executes only after owner approval',async()=>{
 let approved=false;
 api.request.mockImplementation((path:string)=>{if(path.endsWith('/approve')){approved=true;return Promise.resolve({status:'succeeded'});}return Promise.resolve({requests:[{id:'a1',tool:'social_publish',status:approved?'succeeded':'needs_approval',input:{caption:'Reviewed draft',platforms:['linkedin']},output:{}}]});});
 render(ToolApprovals);await screen.findByText(/Reviewed draft/);expect(api.request.mock.calls.every(([path])=>!path.endsWith('/approve'))).toBe(true);
 await fireEvent.click(screen.getByRole('button',{name:'Approve this action'}));await screen.findByText(/social_publish · succeeded/);
 expect(api.request).toHaveBeenCalledWith('/pages/agent-tools/a1/approve',{method:'POST',json:{}});
});
