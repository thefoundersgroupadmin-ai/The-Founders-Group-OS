#!/usr/bin/env python3
"""Install the OS tool gateway into existing task runtimes. Never prints secrets."""
import json,os,urllib.request,urllib.error
from pathlib import Path
ROOT=Path(__file__).resolve().parent.parent
SECRET_VALUES=[]
HOME=Path.home()/'.founderos'
def envfile(path):
 out={}
 if path.exists():
  for line in path.read_text().splitlines():
   line=line.strip().removeprefix('export ')
   if '=' in line and not line.startswith('#'):
    k,v=line.split('=',1);out[k.strip()]=v.strip().strip('"').strip("'")
 return out
ENV={}
for path in [ROOT/'desktop/backend-go/.env',HOME/'.env',HOME/'planted.env']:ENV.update(envfile(path))
ENV.update({k:v for k,v in os.environ.items() if k.startswith('PAPERCLIP_')})
company=ENV['PAPERCLIP_COMPANY_ID'];base=ENV['PAPERCLIP_API_URL'].rstrip('/')
def request(url,method='GET',data=None,headers=None):
 req=urllib.request.Request(url,method=method,headers=headers or {},data=json.dumps(data).encode() if data is not None else None)
 try:
  with urllib.request.urlopen(req,timeout=45) as r:return json.load(r)
 except urllib.error.HTTPError as e:
  detail=e.read().decode(errors='replace')[:1500]
  for hidden in SECRET_VALUES:
   if hidden:detail=detail.replace(hidden,'[redacted]')
  raise RuntimeError('HTTP '+str(e.code)+' from '+urllib.parse.urlparse(url).path+': '+detail) from None

def pc(path,method='GET',data=None):return request(base+'/api'+path,method,data,{'Authorization':'Bearer '+ENV.get('PAPERCLIP_BOARD_KEY',''),'Content-Type':'application/json'})
def items(value,key):return value if isinstance(value,list) else value.get(key,[])
def main():
 key=(HOME/'agent-tools.key').read_text().strip()
 SECRET_VALUES.extend([key,ENV.get('PAPERCLIP_BOARD_KEY','')])
 catalogue=request('http://127.0.0.1:8801/api/founderos/agent-tools/mcp','POST',{'jsonrpc':'2.0','id':1,'method':'tools/list'},{'Authorization':'Bearer '+key,'Content-Type':'application/json'})['result']
 role_tools=catalogue['_meta']['roleTools']
 prefix='/companies/'+company
 secrets=items(pc(prefix+'/secrets'),'secrets');secret=next((s for s in secrets if s.get('key')=='tfg-os-agent-gateway'),None)
 if secret is None:secret=pc(prefix+'/secrets','POST',{'name':'TFG OS agent gateway','key':'tfg-os-agent-gateway','value':key})
 connections=items(pc(prefix+'/tools/connections'),'connections');connection=next((c for c in connections if c['name']=='TFG OS task tools'),None)
 if connection is None:
  connection=pc(prefix+'/tools/connections','POST',{'applicationName':'TFG OS','name':'TFG OS task tools','transport':'mcp_remote','authKind':'api_key','credentialPolicy':'shared','ownership':'customer','status':'active','enabled':True,'transportConfig':{'url':'https://os.thefoundersgroup.net/api/founderos/agent-tools/mcp','headerPolicy':{'metadata':{'forward':['company_id','agent_id','issue_id','run_id']}}},'credentialRefs':[{'name':'OS gateway','secretId':secret['id'],'placement':'header','key':'Authorization','prefix':'Bearer '}]})
 cid=connection['id']
 # The header template and selected execution grant need the same secret.
 # Catalog refresh alone can work while the execution grant is still empty.
 credential_refs=[{'secretId':secret['id'],'configPath':'OS gateway','required':True}]
 if connection.get('credentialSecretRefs')!=credential_refs:
  pc('/tool-connections/'+cid,'PATCH',{'credentialSecretRefs':credential_refs})
 grants=pc('/tool-connections/'+cid+'/grants').get('grants',[])
 if not any(g.get('kind')=='organization' and g.get('status')=='active' and g.get('isDefault') and any(v.get('secretId')==secret['id'] and v.get('configPath')=='OS gateway' for v in g.get('credentialSecretRefs',[])) for g in grants):
  pc('/tool-connections/'+cid+'/grants/installations','POST',{'credentialSecretRefs':credential_refs,'isDefault':True})
 if connection.get('status')=='draft':pc('/tool-connections/'+cid,'PATCH',{'status':'active'})
 pc('/tool-connections/'+cid+'/catalog/refresh','POST',{})
 catalog=items(pc('/tool-connections/'+cid+'/catalog'),'catalog')
 profiles=items(pc(prefix+'/tools/profiles'),'profiles')
 agents=items(pc(prefix+'/agents'),'agents');bound=[]
 for agent in agents:
  names=role_tools.get(agent['name'])
  if not names:continue
  profile_key='tfg-os-'+agent['id'];profile=next((p for p in profiles if p.get('profileKey')==profile_key),None)
  if profile is None:
   entries=[{'selectorType':'catalog_entry','catalogEntryId':c['id'],'connectionId':cid,'effect':'include'} for c in catalog if c.get('toolName') in names or c.get('name') in names]
   if not entries:raise RuntimeError('No indexed OS tools for role '+agent['name'])
   profile=pc(prefix+'/tools/profiles','POST',{'profileKey':profile_key,'name':'OS tools: '+agent['name'],'defaultAction':'deny','entries':entries})
   profile=profile.get('profile',profile)
  if not any(b.get('targetType')=='agent' and b.get('targetId')==agent['id'] for b in profile.get('bindings',[])):
   pc(prefix+'/tools/profiles/'+profile['id']+'/bind','POST',{'targetType':'agent','targetId':agent['id']})
  bound.append({'agentId':agent['id'],'role':agent['name'],'profileId':profile['id']})
 policies=items(pc(prefix+'/tools/policies'),'policies')
 policy_name='TFG OS broker performs its own exact action review'
 if not any(p.get('name')==policy_name and p.get('selectors',{}).get('connectionIds')==[cid] for p in policies):
  pc(prefix+'/tools/policies','POST',{'name':policy_name,'policyType':'allow','priority':50,'selectors':{'connectionIds':[cid]}})
 # Profiles select tools; installs independently make the connection visible.
 existing=pc('/tool-connections/'+cid+'/installs').get('installs',[])
 installs={(v['targetType'],v['targetId']) for v in existing}
 installs.update(('agent',v['agentId']) for v in bound)
 pc('/tool-connections/'+cid+'/installs','PUT',{'installs':[{'targetType':kind,'targetId':target} for kind,target in sorted(installs)]})
 receipt={'companyId':company,'connectionId':cid,'roles':bound,'count':len(bound)}
 path=HOME/'agent-tool-installation.json';path.write_text(json.dumps(receipt));path.chmod(0o600)
 print(json.dumps({'installed':len(bound),'connectionId':cid,'catalogTools':len(catalog)}))
if __name__=='__main__':
 try:main()
 except Exception as e:print(json.dumps({'error':str(e)}));raise SystemExit(1)
