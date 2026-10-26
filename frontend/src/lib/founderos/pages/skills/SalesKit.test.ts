import { fireEvent, render, screen, waitFor, cleanup } from '@testing-library/svelte';
import { afterEach, expect, it, vi } from 'vitest';
import SalesKit from './SalesKit.svelte';
const api=vi.hoisted(()=>({request:vi.fn()}));
vi.mock('$lib/founderos/api',()=>({founderosFetch:api.request,founderosUrl:(s:string)=>'/api/founderos'+s}));
afterEach(()=>{cleanup();vi.resetAllMocks();});
const catalog={version:'0.1.0',paused:false,skills:[{id:'lead-triage',name:'Inbound lead qualification',category:'Prospecting',method:'Apply the client qualification rubric.',output:'Fit and next question.'}]};

it('prepares a prompt with only the supplied client context and does not generate',async()=>{
 api.request.mockImplementation((path:string)=>Promise.resolve(path.endsWith('/sales-kit')?catalog:{prompt:'Prepared client prompt'}));
 render(SalesKit);
 await screen.findByRole('option',{name:'Inbound lead qualification'});
 expect(screen.getByRole('button',{name:'Prepare prompt'})).toBeDisabled();
 await fireEvent.input(screen.getByLabelText('Client workspace label'),{target:{value:'Acme'}});
 await fireEvent.input(screen.getByLabelText('Client profile'),{target:{value:'We sell inventory planning.'}});
 await fireEvent.input(screen.getByLabelText('What do you need?'),{target:{value:'Qualify this lead.'}});
 await fireEvent.input(screen.getByLabelText('Source 1 records'),{target:{value:'Jordan requested a demo.'}});
 await fireEvent.click(screen.getByRole('button',{name:'Prepare prompt'}));
 await screen.findByText('Prepared client prompt');
 expect(api.request).toHaveBeenLastCalledWith('/pages/sales-kit/prepare/lead-triage',expect.objectContaining({json:expect.objectContaining({workspace:'Acme',sources:[{id:'source-1',title:'',text:'Jordan requested a demo.'}]})}));
 expect(api.request.mock.calls.some(([path])=>path.includes('/run/'))).toBe(false);
 expect(screen.getByRole('link',{name:'Download client package'}).getAttribute('href')).toBe('/api/founderos/pages/sales-kit/export');
});

it('leaves package download available when paused and prevents generation',async()=>{
 api.request.mockResolvedValue({...catalog,paused:true});
 render(SalesKit);
 await screen.findByRole('status');
 expect(screen.getByRole('button',{name:'Generate analysis / draft'})).toBeDisabled();
 expect(screen.getByRole('link',{name:'Download client package'})).toBeTruthy();
});

it('shows a failed generation without a successful draft',async()=>{
 api.request.mockResolvedValueOnce(catalog).mockRejectedValueOnce(new Error('AI paused at usage cutoff'));
 render(SalesKit);
 await screen.findByRole('option',{name:'Inbound lead qualification'});
 for(const [label,value] of [['Client workspace label','Acme'],['Client profile','Approved profile'],['What do you need?','Qualify lead'],['Source 1 records','Inbound request']]){
  await fireEvent.input(screen.getByLabelText(label),{target:{value}});
 }
 await fireEvent.click(screen.getByRole('button',{name:'Generate analysis / draft'}));
 await waitFor(()=>expect(screen.getByRole('alert').textContent).toContain('AI paused'));
 expect(screen.queryByRole('button',{name:'Download draft'})).toBeNull();
});
