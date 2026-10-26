import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/svelte';
import { afterEach, expect, it, vi } from 'vitest';
import { writable } from 'svelte/store';
import CommandHistory from './CommandHistory.svelte';
const api=vi.hoisted(()=>({request:vi.fn()}));
vi.mock('$lib/founderos/api',()=>({founderosFetch:api.request}));
vi.mock('$app/stores',()=>({page:writable({url:new URL('http://localhost/os/skills')})}));
vi.mock('$lib/stores/workspaces',()=>({currentWorkspace:writable({id:'hq-id'})}));
afterEach(()=>{cleanup();vi.resetAllMocks();});
it('submits one command with the active workspace and shows its receipt',async()=>{
 api.request.mockImplementation((path:string)=>Promise.resolve(path.endsWith('/ask')?{reply:{content:'Your command is queued.',receipt:{text:'3 workers, 2 workflows'}}}:{commands:[]}));
 render(CommandHistory);
 await fireEvent.input(screen.getByLabelText('Command'),{target:{value:'Review my sales pipeline'}});
 await fireEvent.click(screen.getByRole('button',{name:'Start command'}));
 await screen.findByRole('status');
 expect(api.request).toHaveBeenCalledWith('/pages/conductor/ask',expect.objectContaining({method:'POST',json:expect.objectContaining({workspaceId:'hq-id',message:'Review my sales pipeline',requestId:expect.any(String)})}));
 expect(screen.getByRole('status').textContent).toContain('queued');
});
it('shows actual blocked steps and a report rather than claiming completion',async()=>{
 api.request.mockResolvedValue({commands:[{id:'c1',request:'Review sales',status:'blocked',steps:[{kind:'agent',id:'gmail-worker',status:'blocked'}],report:'# Connection needed\n\nEmail is unavailable.',createdAt:'2026-10-26T12:00:00Z'}]});
 render(CommandHistory);
 await screen.findByText(/blocked · Review sales/);
 await fireEvent.click(screen.getByText(/blocked · Review sales/));
 expect(screen.getByRole('heading',{name:'Connection needed'})).toBeTruthy();
});
it('keeps an empty command from starting work',async()=>{
 api.request.mockResolvedValue({commands:[]});render(CommandHistory);
 await waitFor(()=>expect(api.request).toHaveBeenCalled());
 expect(screen.getByRole('button',{name:'Start command'}).hasAttribute('disabled')).toBe(true);
 expect(api.request.mock.calls.every(([path])=>path.endsWith('/commands'))).toBe(true);
});
