'use strict';
const $=id=>document.getElementById(id);
const fragment=new URLSearchParams(location.hash.slice(1));
const setupToken=fragment.get('setup')||sessionStorage.getItem('omniswitch.setupToken')||'';
if(fragment.get('setup'))sessionStorage.setItem('omniswitch.setupToken',setupToken);
history.replaceState(null,'',location.pathname);
let choices=[],selected=null,busy=false,connection=null,tab='python';
const status=message=>{$('status').textContent=message;};
async function request(path,options={}){
 const controller=new AbortController();const timer=setTimeout(()=>controller.abort(),12000);
 try{const response=await fetch(path,{...options,signal:controller.signal});const payload=await response.json();if(!response.ok)throw new Error(payload.error||'Could not complete this step. Please try again.');return payload;}finally{clearTimeout(timer);}
}
function setBusy(value){busy=value;document.querySelectorAll('#choose button').forEach(b=>b.disabled=value);$('connect-button').textContent=value?'Checking your connection…':'Connect provider ↗';}
function choose(provider){
 if(busy)return;selected=provider;status('');$('connect-form').hidden=false;
 document.querySelectorAll('.provider').forEach(b=>{const active=b.dataset.provider===provider.id;b.classList.toggle('selected',active);b.setAttribute('aria-pressed',String(active));});
 $('provider-label').textContent='Connect '+provider.name;
 $('detected-row').hidden=!provider.detected;$('use-env').checked=provider.detected;
 $('url-row').hidden=provider.id!=='local';$('key-label').textContent=provider.id==='local'?'API key (optional)':'API key';
 $('provider-key').value='';$('provider-key').disabled=provider.detected;$('model').value='';
 $('connect-button').textContent='Connect '+provider.name+' ↗';
 if(!provider.detected&&provider.id!=='local')$('provider-key').focus();
}
async function connect(provider){
 if(busy)return;status('');setBusy(true);
 try{
  const payload=await request('/api/connect',{method:'POST',headers:{'Content-Type':'application/json','Authorization':'Bearer '+setupToken},body:JSON.stringify({provider:provider.id,key:$('provider-key').value.trim(),base_url:$('base-url').value.trim(),model:$('model').value.trim(),use_env:provider.detected&&$('use-env').checked})});
  connection=payload;$('provider-key').value='';sessionStorage.removeItem('omniswitch.setupToken');
  sessionStorage.setItem('omniswitch.playgroundKey',payload.gateway_key);
  $('choose').hidden=true;$('ready').hidden=false;$('step-connect').classList.remove('current');$('step-ready').classList.add('current');
  $('connected-provider').textContent=payload.provider;$('demo-notice').hidden=!payload.demo;
  $('request-note').textContent=payload.demo?'A simulated response. No provider is called.':'This calls your provider and uses its normal pricing.';
  renderSnippet();window.scrollTo({top:0,behavior:'smooth'});
 }catch(error){status(error.name==='AbortError'?'The connection took too long. Check your network or try again.':error.message);}finally{setBusy(false);}
}
const quote=value=>JSON.stringify(value);
function snippets(){
 const base=connection.base_url,key=connection.app_key,model='default';
 return {
  python:{install:'Install once: pip install openai',code:`from openai import OpenAI\n\nclient = OpenAI(\n    base_url=${quote(base)},\n    api_key=${quote(key)},\n)\n\nresponse = client.chat.completions.create(\n    model="${model}",\n    messages=[{"role": "user", "content": "Hello!"}],\n)\nprint(response.choices[0].message.content)`},
  javascript:{install:'Install once: npm install openai',code:`import OpenAI from "openai";\n\nconst client = new OpenAI({\n  baseURL: ${quote(base)},\n  apiKey: ${quote(key)},\n});\n\nconst response = await client.chat.completions.create({\n  model: "${model}",\n  messages: [{ role: "user", content: "Hello!" }],\n});\nconsole.log(response.choices[0].message.content);`},
  env:{install:'Use with an OpenAI-compatible client. Choose model default.',code:`OPENAI_BASE_URL=${base}\nOPENAI_API_KEY=${key}\nOMNISWITCH_MODEL=default`},
  curl:{install:'No SDK needed.',code:`curl ${quote(base+'/chat/completions')} \\\n  -H ${quote('Authorization: Bearer '+key)} \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"default","messages":[{"role":"user","content":"Hello!"}]}'`},
  langchain:{install:'Install once: pip install langchain-openai',code:`from langchain_openai import ChatOpenAI\n\nmodel = ChatOpenAI(\n    model="default",\n    base_url=${quote(base)},\n    api_key=${quote(key)},\n)\nprint(model.invoke("Hello!").content)`},
  llamaindex:{install:'Install once: pip install llama-index-llms-openai-like',code:`from llama_index.llms.openai_like import OpenAILike\n\nllm = OpenAILike(\n    model="default",\n    api_base=${quote(base)},\n    api_key=${quote(key)},\n    is_chat_model=True,\n)\nprint(llm.complete("Hello!"))`}
 };
}
function renderSnippet(){const snippet=snippets()[tab];$('install-hint').textContent=snippet.install;$('snippet').textContent=snippet.code;document.querySelectorAll('[data-tab]').forEach(b=>b.setAttribute('aria-selected',String(b.dataset.tab===tab)));}
async function copyCode(){try{await navigator.clipboard.writeText($('snippet').textContent);$('copy').textContent='Copied ✓';setTimeout(()=>$('copy').textContent='Copy integration ⧉',1600);}catch{status('Clipboard is unavailable here. Select the code to copy it.');}}
async function waitForGateway(){for(let i=0;i<60;i++){try{const r=await fetch('/readyz');if(r.ok)return;}catch{}await new Promise(resolve=>setTimeout(resolve,150));}throw new Error('Your gateway is still starting. Check the terminal, then try again.');}
$('connect-form').addEventListener('submit',e=>{e.preventDefault();if(selected)connect(selected);});
$('use-env').addEventListener('change',()=>$('provider-key').disabled=$('use-env').checked);
$('show-key').addEventListener('click',()=>{const show=$('provider-key').type==='password';$('provider-key').type=show?'text':'password';$('show-key').textContent=show?'Hide':'Show';$('show-key').setAttribute('aria-label',show?'Hide API key':'Show API key');});
$('demo').addEventListener('click',()=>connect(choices.find(p=>p.id==='demo')));
$('copy').addEventListener('click',copyCode);
document.querySelectorAll('[data-tab]').forEach(b=>b.addEventListener('click',()=>{tab=b.dataset.tab;renderSnippet();}));
document.querySelectorAll('[data-framework]').forEach(b=>b.addEventListener('click',()=>{tab=b.dataset.framework;renderSnippet();$('snippet').focus();}));
$('download-env').addEventListener('click',()=>{const blob=new Blob([snippets().env.code+'\n'],{type:'text/plain'});const link=document.createElement('a');const url=URL.createObjectURL(blob);link.href=url;link.download='.env.omniswitch';link.click();setTimeout(()=>URL.revokeObjectURL(url),1000);});
$('try').addEventListener('click',async()=>{status('');$('try').disabled=true;$('reply').hidden=true;try{await waitForGateway();const result=await request('/v1/chat/completions',{method:'POST',headers:{'Content-Type':'application/json','Authorization':'Bearer '+connection.app_key},body:JSON.stringify({model:'default',messages:[{role:'user',content:$('prompt').value}],max_tokens:80})});$('reply').textContent=result.choices?.[0]?.message?.content||'Request completed.';$('reply').hidden=false;}catch(error){status(error.message);}finally{$('try').disabled=false;}});
(async()=>{try{if(!setupToken)throw new Error('Open the setup link printed in your terminal to continue.');const data=await request('/api/setup',{headers:{'Authorization':'Bearer '+setupToken}});choices=data.providers;for(const p of choices.filter(p=>p.id!=='demo')){const b=document.createElement('button');b.type='button';b.className='provider';b.dataset.provider=p.id;b.setAttribute('aria-pressed','false');const symbol=document.createElement('span');symbol.className='symbol';symbol.textContent={openai:'O',anthropic:'A',google:'G',groq:'g',openrouter:'↗',local:'⌘'}[p.id];const text=document.createElement('span');const title=document.createElement('strong');title.textContent=p.name;const note=document.createElement('small');note.textContent=p.detected?'Key detected ✓':p.note;if(p.detected)note.className='detected-tag';text.append(title,note);b.append(symbol,text);b.addEventListener('click',()=>choose(p));$('providers').append(b);}const detected=choices.find(p=>p.detected);if(detected)choose(detected);}catch(error){status(error.message);$('demo').disabled=true;}})();
