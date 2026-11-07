#!/usr/bin/env node
// Dependency-free synthetic interaction tests of the actual shipped controller.
// A DOM shim checks state/races, not layout, native focus trapping or real keyboards.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
const source = fs.readFileSync(new URL('../assets/service-editor.js', import.meta.url), 'utf8');
const browserSource = fs.readFileSync(new URL('../assets/service-editor-browser.js', import.meta.url), 'utf8');
const rendererSource = fs.readFileSync(new URL('../handlers.go', import.meta.url), 'utf8');
const searchSource = [...rendererSource.matchAll(/\(function \(\) \{[\s\S]*?\}\(\)\);/g)].map(m=>m[0]).find(s=>s.includes('function cardHaystack'));
assert.ok(searchSource, 'actual renderer search controller found');
let checks = 0;
function check(value, message) { assert.ok(value, message); checks++; }
class Element {
  constructor(id) { this.id = id; this.attrs = {}; this.listeners = {}; this.value = ''; this.textContent = ''; this.hidden = false; this.disabled = false; this.isConnected = true; this.childNodes=[]; this.tagName='DIV'; this.className=''; }
  get firstChild() { return this.childNodes[0] || null; }
  appendChild(e) { if (e.parent) e.remove(); this.childNodes.push(e); e.parent=this; e.document=this.document; return e; }
  remove() { if (this.parent) this.parent.childNodes=this.parent.childNodes.filter(e=>e!==this); this.parent=null; }
  replaceWith(e) { const parent=this.parent, i=parent.childNodes.indexOf(this); parent.childNodes[i]=e; e.parent=parent; e.document=this.document; this.parent=null; }
  querySelector(selector) { for (const e of this.childNodes) { if ((selector[0]==='.' && e.className.split(' ').includes(selector.slice(1))) || (selector==='[data-edit-service]' && e.attrs['data-edit-service'])) return e; const found=e.querySelector(selector); if (found) return found; } return null; }
  getAttribute(k) { return this.attrs[k] ?? null; }
  setAttribute(k,v) { this.attrs[k] = String(v); }
  addEventListener(k,f) { (this.listeners[k] ||= []).push(f); }
  emit(k,event={}) { event.target ||= this; event.preventDefault ||= () => {event.prevented = true;}; for (const f of this.listeners[k] || []) f(event); return event; }
  closest(selector) {
    if (selector === '[data-edit-service]') return this.attrs['data-edit-service'] ? this : null;
    if (selector === '[hidden]') return this.hidden || this.searchHidden ? this : null;
    return null;
  }
  focus() { this.document.activeElement = this; }
}
function fixture(mode='shared', options={}) {
  const ids = ['service-editor','service-editor-form','service-editor-fields','service-editor-name','service-editor-url','service-editor-icon','service-editor-preview','service-editor-status','service-editor-save','service-editor-reset','service-editor-reload','service-editor-cancel','service-editor-announcement','service-editor-defaults','service-editor-title','portal-content','services-heading','portal-search-input','portal-search-clear','portal-search-status','portal-search-no-results'];
  const elements = Object.fromEntries(ids.map(id => [id,new Element(id)]));
  const document = new Element('document'); document.body = new Element('body'); document.getElementById = id => elements[id] || null;
  const root = elements['portal-content']; root.attrs = {'data-identity-scope':mode==='browser' ? (options.scope || 'a'.repeat(64)) : 'alpha','data-service-editing':'true','data-service-edit-mode':mode};
  elements['service-editor'].attrs['data-edit-mode']=mode;
  const button = new Element('edit-3'); button.attrs['data-edit-service']='3'; root.cards=[button];
  root.querySelector = selector => root.cards.find(b => selector === '[data-edit-service="'+b.attrs['data-edit-service']+'"]') || null;
  root.querySelectorAll = () => root.cards;
  document.querySelectorAll = () => root.cards;
  for (const e of [...Object.values(elements),button]) e.document=document;
  const icon = elements['service-editor-icon']; icon.options = ['','generic','grafana'].map(v=>{const e=new Element(v); e.value=v; e.attrs['data-icon-path']='/static/service-icons/'+(v || 'generic')+(v==='grafana'?'.png':'.svg'); return e;});
  Object.defineProperty(icon,'selectedIndex',{get:()=>Math.max(0,['','generic','grafana'].indexOf(icon.value))});
  const dialog = elements['service-editor']; dialog.open=false;
  const queuedClose=[];
  dialog.showModal = () => {dialog.open=true; elements['service-editor-title'].focus();};
  dialog.close = () => {if (!dialog.open) return;dialog.open=false;queuedClose.push(()=>dialog.emit('close'));};
  elements['service-editor-form'].reset = () => {for (const id of ['service-editor-name','service-editor-url','service-editor-icon']) elements[id].value='';};
  const requests=[], triggers=[], confirmations=[];
  const state = {elements,document,root,button,dialog,requests,triggers,confirmations,confirm:true,queuedClose};
  const window = {AbortController,confirm:text=>{confirmations.push(text);return state.confirm;},htmx:{trigger:(el,name)=>triggers.push(name)}};
  const fetch = (path, options) => new Promise((resolve,reject)=>requests.push({path,options,resolve:(status,data)=>resolve({status,ok:status>=200&&status<300,json:()=>data instanceof Error ? Promise.reject(data) : Promise.resolve(data)}),reject}));
  class DOMParser { parseFromString() {return {getElementById:()=>state.parsed || root};} }
  const storage = options.storage || new Map();
  const windowEvents=new Element('window'); window.addEventListener=(k,f)=>windowEvents.addEventListener(k,f);
  let storageCalls=0;
  window.localStorage={getItem:k=>{storageCalls++;if(options.storageError || mode==='shared') throw new Error('storage unavailable');return storage.get(k) ?? null;},setItem:(k,v)=>{storageCalls++;if(options.storageError || options.saveError || mode==='shared') throw new Error('storage unavailable');storage.set(k,v);},removeItem:k=>{storageCalls++;if(options.storageError || mode==='shared') throw new Error('storage unavailable');storage.delete(k);}};
  state.storage=storage; state.storageEvent=(key)=>windowEvents.emit('storage',{key}); state.key='velociportal-service-presentation-v1:'+root.attrs['data-identity-scope'];state.options=options;state.storageCalls=()=>storageCalls;
  if (options.noAbort) delete window.AbortController;
  document.createElement=tag=>{const e=new Element('');e.tagName=tag.toUpperCase();e.document=document;return e;};
  if(mode==='browser') {
    state.fresh=(name='Server default',url='https://service.example.test',icon='grafana')=>{
      const item=document.createElement('article');item.attrs={'data-service-id':'3','data-default-name':name,'data-default-url':url,'data-default-icon':icon,'data-default-link-state':url?'ready':'needs_metadata','data-default-hostname':url?'service.example.test':'','data-default-scheme':url?'https':''};
      const main=document.createElement(url?'a':'div'); main.className='service-main'+(url?'':' card-unlinked'); if(url)main.attrs.href=url;
      const head=document.createElement('span');head.className='card-head';const n=document.createElement('span');n.className='card-name';n.textContent=name;head.appendChild(n);
      const badge=document.createElement('span');badge.className='badge';badge.textContent='https';const image=document.createElement('img');image.className='service-icon';const health=document.createElement('span');health.className='health';health.textContent='Backend check passed';
      main.appendChild(image);main.appendChild(head);main.appendChild(badge);main.appendChild(health);item.appendChild(main);item.appendChild(button);root.cards=[item];state.card=item;return item;
    };
    state.fresh();
    root.querySelector=selector=>{if(selector==='[data-edit-service="3"]') return root.cards.length?button:null;return root.cards.find(c=>selector==='[data-service-id="'+c.attrs['data-service-id']+'"]') || null;};
    root.querySelectorAll=selector=>selector==='[data-default-name][data-service-id]' ? root.cards : root.cards.length ? [button] : [];
    document.querySelectorAll=selector=>{
      if(selector==='#services .card')return root.cards;
      if(selector==='#services .card, #services .card-unlinked')return root.cards.concat(root.cards.map(c=>c.querySelector('.card-unlinked')).filter(Boolean));
      if(selector==='[data-edit-service]')return root.cards.length ? [button] : [];
      return [];
    };
    elements['portal-search-input'].value=options.search || '';
  }
  const context = vm.createContext({document,window,fetch,AbortController,DOMParser,console,URL,TextEncoder});
  state.context=context;
  if(mode==='browser') { vm.runInContext(searchSource,context);vm.runInContext(browserSource,context); }
  vm.runInContext(source, context);
  return state;
}
const tick=()=>new Promise(resolve=>setImmediate(resolve));
async function settled() {await tick();await tick();}
function form(scope='alpha') {return {id:3,identity_scope:scope,fields:{name:'Shared',url:'https://service.example.test',icon:'grafana'},defaults:{name:'service.example.test',url:'https://service.example.test',icon:'generic'},icons:['generic','grafana'],revision:'rev',target_revision:'target',csrf:'csrf'};}
function click(s,el=s.button) {s.document.emit('click',{target:el});}
async function opened(s) {click(s);s.requests.at(-1).resolve(200,form());await settled();}
function send(s) {s.elements['service-editor-form'].emit('submit');}
function cancel(s) {s.elements['service-editor-cancel'].emit('click');}

{
 const s=fixture();click(s);check(s.dialog.open,'native dialog opens');check(s.elements['service-editor-save'].disabled,'loading disables save');check(s.document.activeElement.id==='service-editor-title','picker does not steal initial focus');
 s.requests[0].resolve(200,form());await settled();check(s.elements['service-editor-name'].value==='Shared','GET populates selected service');check(!s.elements['service-editor-save'].disabled,'loaded form enables save');check(s.elements['service-editor-preview'].src==='/static/service-icons/grafana.png','finite local preview');
 s.elements['service-editor-name'].value='Draft';s.button.searchHidden=true;s.root.emit('unused');s.document.body.emit('htmx:afterSwap');check(s.dialog.open && s.elements['service-editor-name'].value==='Draft','search-hidden and harmless poll preserve draft');
 s.confirm=false;cancel(s);check(s.dialog.open,'dirty close requires confirmation');s.confirm=true;cancel(s);check(!s.dialog.open && s.elements['service-editor-name'].value==='','close clears draft');check(s.requests[0].options.signal.aborted,'close aborts request signal');
}
{
 const s=fixture();click(s);const old=s.requests[0];cancel(s);click(s);const current=s.requests[1];s.queuedClose.shift()();
 old.resolve(200,form());await settled();check(s.dialog.open && s.elements['service-editor-name'].value==='','late GET cannot insert old values or clear reopened session');
 current.resolve(200,{...form(),fields:{name:'New',url:'',icon:''}});await settled();check(s.elements['service-editor-name'].value==='New','current reopened request works');
}
{
 const s=fixture();await opened(s);s.elements['service-editor-name'].value='Draft';send(s);check(!s.elements['service-editor-announcement'].textContent,'no optimistic save announcement');check(s.triggers.includes('htmx:abort'),'cancel pre-save fragment poll');
 const poll={detail:{target:s.root,xhr:{responseText:'synthetic'},shouldSwap:true}};s.document.body.emit('htmx:beforeSwap',poll);check(!poll.detail.shouldSwap,'same-identity poll cannot overwrite pending save');
 const post=s.requests.at(-1);const body=JSON.parse(post.options.body);check(body.action==='save'&&body.name==='Draft'&&body.csrf==='csrf','save sends selected fields and opaque tokens');check(post.options.headers['Content-Type']==='application/json','strict JSON type');
 post.resolve(409,{error:'conflict'});await settled();check(s.dialog.open && s.elements['service-editor-name'].value==='Draft','conflict keeps draft');check(s.elements['service-editor-save'].disabled&&!s.elements['service-editor-reload'].hidden,'conflict requires review/reload');
 s.confirm=false;s.elements['service-editor-reload'].emit('click');check(s.requests.length===2,'reload does not silently discard draft');s.confirm=true;s.elements['service-editor-reload'].emit('click');s.requests.at(-1).resolve(200,{...form(),revision:'new-rev'});await settled();send(s);s.requests.at(-1).resolve(200,{status:'saved'});await settled();
 check(!s.dialog.open && s.elements['service-editor-announcement'].textContent.includes('saved for everyone'),'announce only confirmed save');check(s.triggers.includes('service-metadata-saved'),'authorized fragment refreshed');
}
{
 const s=fixture();await opened(s);s.elements['service-editor-reset'].emit('click');check(s.confirmations.at(-1).includes('Category and order remain unchanged'),'reset confirmation explains preservation');
 const body=JSON.parse(s.requests.at(-1).options.body);check(Object.keys(body).sort().join(',')==='action,csrf,revision,target_revision'&&body.action==='reset','reset omits editable fields');s.requests.at(-1).resolve(200,{status:'saved'});await settled();check(s.elements['service-editor-announcement'].textContent.includes('Category and order are unchanged'),'confirmed reset truthful');
}
{
 const s=fixture();await opened(s);s.elements['service-editor-name'].value='Secret draft';send(s);const pending=s.requests.at(-1);s.root.attrs['data-identity-scope']='beta';s.elements['portal-search-input'].value='Secret search';s.document.body.emit('htmx:afterSwap');
 check(!s.dialog.open&&s.elements['service-editor-name'].value==='','identity change clears draft');check(s.elements['portal-search-input'].value==='','identity change clears search');check(s.button.hidden,'identity change hides stale controls');
 pending.resolve(200,{status:'saved'});await settled();check(!s.elements['service-editor-announcement'].textContent.includes('saved for everyone'),'late save cannot announce for another identity');
}
{
 const s=fixture();click(s);const pending=s.requests[0];s.root.cards=[];s.document.body.emit('htmx:afterSwap');check(!s.dialog.open,'loss of visible ID closes');pending.resolve(200,form());await settled();check(s.elements['service-editor-name'].value==='','late GET after access loss suppressed');
}
{
 const s=fixture();click(s);s.requests[0].resolve(200,form('beta'));await settled();check(!s.dialog.open && s.elements['service-editor-name'].value==='','GET identity scope mismatch clears without inserting');
}
{
 const s=fixture();await opened(s);send(s);s.requests.at(-1).resolve(503,{error:'uncertain'});await settled();check(s.dialog.open&&s.elements['service-editor-save'].disabled&&s.elements['service-editor-reload'].hidden,'uncertain save cannot retry from form');check(s.elements['service-editor-status'].textContent.includes('Do not retry'),'uncertain outcome explained');
}
{
 const s=fixture();await opened(s);send(s);s.requests.at(-1).reject(new Error('network'));await settled();check(s.elements['service-editor-save'].disabled && !s.elements['service-editor-reload'].hidden,'lost confirmation requires reload/review');check(s.elements['service-editor-name'].value==='Shared','network failure preserves form');
}
{
 const s=fixture();await opened(s);s.elements['service-editor-name'].value='Draft';send(s);s.requests.at(-1).resolve(400,{error:'invalid_fields'});await settled();check(!s.elements['service-editor-save'].disabled && s.elements['service-editor-name'].value==='Draft','invalid fields preserve editable draft');
 s.dialog.emit('cancel');check(!s.dialog.open,'Escape follows dirty-close confirmation');
}
{
 const s=fixture();await opened(s);s.document.body.emit('htmx:responseError',{detail:{target:s.root,xhr:{status:403}}});check(!s.dialog.open&&s.elements['service-editor-name'].value==='','trusted-identity poll failure clears form');
}
{
 const s=fixture();await opened(s);send(s);s.requests.at(-1).resolve(403,new SyntaxError('plain text identity response'));await settled();check(!s.dialog.open && s.elements['service-editor-name'].value==='','plain-text outer identity denial clears without JSON parsing');
}
{
 const s=fixture();await opened(s);s.elements['service-editor-name'].value='Draft';send(s);s.requests.at(-1).resolve(503,{error:'unavailable'});await settled();check(s.dialog.open && s.elements['service-editor-name'].value==='Draft' && s.elements['service-editor-save'].disabled,'stale snapshot preserves draft and disables save');
}
{
 const s=fixture();await opened(s);const parsed=new Element('portal-content');parsed.attrs={'data-identity-scope':'alpha','data-service-editing':'false'};parsed.querySelector=()=>null;s.parsed=parsed;s.document.body.emit('htmx:beforeSwap',{detail:{target:s.root,xhr:{responseText:'synthetic'},shouldSwap:true}});check(!s.dialog.open,'loss of editor membership detected before fragment swap');
}
check(!/localStorage|sessionStorage|indexedDB/.test(source),'shared controller stores no drafts/tokens');
{
 const s=fixture();await opened(s);send(s);s.requests.at(-1).resolve(200,{status:'saved'});await settled();check(s.storageCalls()===0,'shared mode never invokes browser storage');
}
{
 const s=fixture('browser',{noAbort:true});click(s);check(s.dialog.open && !s.elements['service-editor-save'].disabled,'browser mode works without AbortController');
 check(s.elements['service-editor-preview'].src==='/static/service-icons/grafana.png','empty browser icon previews current metadata default, not generic');
 s.elements['service-editor-name'].value='My <img onerror=evil> service';s.elements['service-editor-url'].value='https://personal.example.test/app';s.elements['service-editor-icon'].value='generic';send(s);
 check(!s.dialog.open && s.requests.length===0,'browser save closes without any edit API request');
 const raw=s.storage.get(s.key), entry=JSON.parse(raw).services['3'];
 check(Object.keys(entry).sort().join(',')==='icon,name,url' && !/csrf|revision|identity|Server default|Backend check/.test(raw),'only user fields persisted, not catalog or tokens');
 check(s.card.querySelector('.card-name').textContent===entry.name,'hostile-looking name is text, not HTML');
 check(s.card.getAttribute('data-service-hostname')==='personal.example.test' && s.card.querySelector('.service-domain').textContent==='personal.example.test','local URL updates search hostname and visible domain');
 check(s.card.querySelector('.service-icon').attrs.src==='/static/service-icons/generic.svg','icon path comes from fixed choices');
 check(s.card.querySelector('.health').textContent==='Backend check passed','health markup unchanged');
 const reloaded=fixture('browser',{storage:s.storage});check(reloaded.card.querySelector('.card-name').textContent===entry.name,'reload reapplies browser preferences');
 const other=fixture('browser',{storage:s.storage,scope:'b'.repeat(64)});check(other.card.querySelector('.card-name').textContent==='Server default','different exact identity digest has independent preferences');
 click(s);s.elements['service-editor-reset'].emit('click');check(!s.storage.has(s.key) && s.card.querySelector('.card-name').textContent==='Server default','reset removes local data and restores server defaults');
 check(s.card.querySelector('.service-icon').attrs.src==='/static/service-icons/grafana.png' && !s.card.querySelector('.service-domain'),'reset restores metadata icon and original presentation');
}
{
 const s=fixture('browser');s.fresh('*.example.test','');s.document.body.emit('htmx:afterSwap');click(s);s.elements['service-editor-url'].value='https://linked.example.test';send(s);
 check(s.card.querySelector('.service-main').tagName==='A' && s.card.querySelector('.service-main').attrs.href==='https://linked.example.test','unlinked inner div safely becomes link');
 check(s.card.querySelector('[data-edit-service]')===s.button,'edit stays sibling outside navigation');
 click(s);s.elements['service-editor-reset'].emit('click');check(s.card.querySelector('.service-main').tagName==='DIV' && s.card.querySelector('.badge').textContent==='link needed','reset unlinked card removes anchor');
}
{
 const s=fixture('browser');click(s);s.elements['service-editor-name'].value='Local';send(s);
 s.fresh('New metadata','https://new-default.example.test','generic');s.document.body.emit('htmx:afterSwap');check(s.card.querySelector('.card-name').textContent==='Local','fresh polling fragment reapplies override');
 click(s);s.elements['service-editor-name'].value='Draft';s.button.searchHidden=true;s.document.body.emit('htmx:afterSwap');check(s.dialog.open && s.elements['service-editor-name'].value==='Draft','harmless poll/search-hidden preserve browser draft');
 cancel(s);click(s);s.elements['service-editor-name'].value='';send(s);check(s.card.querySelector('.card-name').textContent==='New metadata','blank restores latest immutable server default, not customized DOM');
}
{
 const s=fixture('browser');click(s);s.elements['service-editor-name'].value='Dirty';
 s.storage.set(s.key,JSON.stringify({version:1,services:{3:{name:'Other tab'},4:{name:'Unseen'}}}));s.storageEvent(s.key);
 check(s.elements['service-editor-name'].value==='Dirty','cross-tab event does not overwrite dirty form');send(s);
 check(JSON.parse(s.storage.get(s.key)).services['4'].name==='Unseen' && s.root.cards.length===1,'save rereads map and preserves inert IDs without creating cards');
 click(s);s.storage.set(s.key,JSON.stringify({version:1,services:{3:{name:'Clean-tab update'}}}));s.storageEvent(s.key);check(s.elements['service-editor-name'].value==='Clean-tab update','clean form can follow cross-tab update');
}
for(const loss of ['identity','mode','access','denial']) {
 const s=fixture('browser');click(s);s.elements['service-editor-name'].value='Private draft';
 if(loss==='identity')s.root.attrs['data-identity-scope']='b'.repeat(64);
 if(loss==='mode')s.root.attrs['data-service-edit-mode']='shared';
 if(loss==='access')s.root.cards=[];
 if(loss==='denial')s.document.body.emit('htmx:responseError',{detail:{target:s.root,xhr:{status:403}}});
 s.document.body.emit('htmx:afterSwap');send(s);
 check(!s.dialog.open && s.elements['service-editor-name'].value==='' && !s.storage.has(s.key),loss+' loss invalidates draft with no write');
 if(loss==='mode'||loss==='identity'){s.root.attrs['data-service-edit-mode']='browser';s.root.attrs['data-identity-scope']='a'.repeat(64);click(s);check(!s.dialog.open,'mode/identity replacement requires full reload');}
}
{
 const options={storageError:true},s=fixture('browser',options);click(s);s.elements['service-editor-name'].value='Draft';send(s);
 check(s.dialog.open && s.elements['service-editor-name'].value==='Draft' && s.elements['service-editor-status'].textContent.includes('Not saved') && !s.elements['service-editor-announcement'].textContent,'unavailable storage preserves draft and never claims saved');
 options.storageError=false;send(s);check(!s.dialog.open,'storage recovery saves existing draft');
}
{
 const scope='a'.repeat(64),storage=new Map([['velociportal-service-presentation-v1:'+scope,JSON.stringify({version:1,services:{3:{name:'Personal find',url:'https://find.example.test'}}})]]);
 const s=fixture('browser',{storage,search:'personal find'});check(!s.card.hidden && s.elements['portal-search-no-results'].hidden,'initial preferences apply before final search recomputation');
 s.elements['portal-search-input'].value='find.example.test';s.elements['portal-search-input'].emit('input');check(!s.card.hidden,'local URL hostname is searchable');
 s.fresh('Server refresh','');s.document.body.emit('htmx:afterSwap');check(!s.card.hidden && s.card.querySelector('.service-main').tagName==='A','poll reapplies local link then recomputes actual search');
 click(s);s.elements['service-editor-reset'].emit('click');check(s.card.hidden && !s.elements['portal-search-no-results'].hidden,'reset recomputes no-results against current defaults');
 check(!rendererSource.includes('querySelectorAll("#services .card, #services .card-unlinked")'),'nested unlinked navigation is not counted as another service');
}
{
 const s=fixture('browser',{saveError:true});click(s);s.elements['service-editor-name'].value='Quota draft';send(s);check(s.dialog.open && !s.storage.has(s.key) && s.elements['service-editor-status'].textContent.includes('Not saved'),'write quota error keeps draft without optimistic DOM/persistence');
}
{
 const s=fixture('browser');s.fresh('Go-supported default','https://example.test:99999','generic');s.document.body.emit('htmx:afterSwap');check(s.card.querySelector('.card-name').textContent==='Go-supported default','Go defaults outside WHATWG subset do not crash browser application');
}
const unsafeURLs=['javascript:alert(1)','//evil.test','https://user:pass@evil.test','https://@evil.test','https://evil.test\\\\@safe.test',' https://evil.test','https://evil.test\n/a','https://EVIL.test','https://127.1','https://evil.test:443','https://evil.test/a/../b','https://evil.test/%2e%2e/b','https://evil.test/%0a','https://evil.test/%zz','https://*.evil.test','https://evil.test/'+ 'a'.repeat(2048),'https://evil.test/\ud800'];
for(const url of unsafeURLs){const s=fixture('browser');click(s);s.elements['service-editor-url'].value=url;send(s);check(s.dialog.open && !s.storage.has(s.key),'unsafe URL rejected on save: '+JSON.stringify(url.slice(0,60)));}
for(const name of [' padded','x\u0085y','\ud800','\udfff','😀'.repeat(121)]){const s=fixture('browser');click(s);s.elements['service-editor-name'].value=name;send(s);check(s.dialog.open && !s.storage.has(s.key),'invalid Unicode name rejected');}
for(const url of ['http://example.test','https://example.test/path?q=ok#fragment','https://[fd7a:115c:a1e0::1]:8443/path','https://example.test/%E2%98%83']){const s=fixture('browser');click(s);s.elements['service-editor-name'].value='😀'.repeat(120);s.elements['service-editor-url'].value=url;send(s);check(!s.dialog.open,'safe URL and 120 Unicode code points accepted');}
const hostileStorage=['{',JSON.stringify({version:2,services:{}}),JSON.stringify({version:1,services:{'-1':{name:'bad'}}}),JSON.stringify({version:1,services:{3:{icon:'../../evil'}}}),JSON.stringify({version:1,services:{3:{name:123}}}),JSON.stringify({version:1,services:{3:{csrf:'token'}}}),JSON.stringify({version:1,services:{3:{url:'javascript:alert(1)'}}}),JSON.stringify({version:1,services:{3:{name:'\ud800'}}}),JSON.stringify({version:1,services:Object.fromEntries(Array.from({length:1025},(_,i)=>[i+1,{name:'x'}]))}), ' '.repeat(256*1024+1),JSON.stringify({version:1,services:{'__proto__':{name:'bad'}},extra:true})];
for(const raw of hostileStorage){const storage=new Map([['velociportal-service-presentation-v1:'+'a'.repeat(64),raw]]);const s=fixture('browser',{storage});check(s.card.querySelector('.card-name').textContent==='Server default' && s.root.cards.length===1,'hostile storage ignored without hidden-card restoration');click(s);s.elements['service-editor-name'].value='Draft';send(s);check(s.dialog.open && storage.get(s.key)===raw,'hostile storage cannot be silently overwritten by save');}
// Independent boundary/lifecycle regressions: exercise the shipped adapter,
// not a second implementation of its validators or storage policy.
{
 const s=fixture('browser');click(s);s.elements['service-editor-name'].value='Local';s.elements['service-editor-url'].value='https://local.example.test';s.elements['service-editor-icon'].value='generic';send(s);
 s.fresh('Latest metadata','https://latest.example.test','grafana');s.document.body.emit('htmx:afterSwap');click(s);
 s.elements['service-editor-name'].value='';s.elements['service-editor-url'].value='';s.elements['service-editor-icon'].value='';send(s);
 check(!s.storage.has(s.key) && s.card.querySelector('.service-main').attrs.href==='https://latest.example.test','all blank fields remove overrides and restore latest metadata link');
 check(s.card.querySelector('.card-name').textContent==='Latest metadata' && s.card.querySelector('.service-icon').attrs.src==='/static/service-icons/grafana.png','all blanks restore latest metadata name/icon');
 check(s.requests.length===0,'browser open/save/poll/blank cycle makes zero edit requests');
}
{
 const s=fixture('browser');click(s);s.elements['service-editor-name'].value='Dirty clear-event draft';
 s.storageEvent(null);check(s.elements['service-editor-name'].value==='Dirty clear-event draft','cross-tab clear event never discards dirty form');
 s.storage.set('other-identity',JSON.stringify({version:1,services:{3:{name:'Wrong identity'}}}));s.storageEvent('other-identity');
 check(s.card.querySelector('.card-name').textContent==='Server default' && s.elements['service-editor-name'].value==='Dirty clear-event draft','unrelated identity storage events are ignored');
}
{
 const s=fixture('browser');click(s);s.elements['service-editor-name'].value='Keep saved';send(s);click(s);
 s.options.storageError=true;s.elements['service-editor-reset'].emit('click');
 check(s.dialog.open && JSON.parse(s.storage.get(s.key)).services['3'].name==='Keep saved' && s.elements['service-editor-status'].textContent.includes('Not saved'),'blocked reset retains persisted preference and reports not saved');
}
{
 const storage=new Map(),key='velociportal-service-presentation-v1:'+'a'.repeat(64);
 storage.set(key,JSON.stringify({version:1,services:Object.fromEntries(Array.from({length:1024},(_,i)=>[i+10,{name:'x'}]))}));
 const s=fixture('browser',{storage});click(s);s.elements['service-editor-name'].value='Overflow';send(s);
 check(s.dialog.open && Object.keys(JSON.parse(storage.get(key)).services).length===1024,'save cannot create entry1025');
 s.elements['service-editor-reset'].emit('click');check(!s.dialog.open && Object.keys(JSON.parse(storage.get(key)).services).length===1024,'reset at entry bound preserves unrelated inert IDs');
}
{
 const key='velociportal-service-presentation-v1:'+'a'.repeat(64),services=Object.fromEntries(Array.from({length:524},(_,i)=>[i+10,{name:'😀'.repeat(120)}]));
 const raw=JSON.stringify({version:1,services}),storage=new Map([[key,raw]]);
 const s=fixture('browser',{storage});click(s);s.elements['service-editor-url'].value='https://example.test/'+ 'a'.repeat(2027);send(s);
 check(s.dialog.open && storage.get(key)===raw,'save enforces serialized UTF8 bytes, not UTF16 string length');
}
for(const id of ['0','03','9007199254740992']) {
 const key='velociportal-service-presentation-v1:'+'a'.repeat(64),raw=JSON.stringify({version:1,services:{[id]:{name:'Bad ID'},3:{name:'Should not partially load'}}});
 const s=fixture('browser',{storage:new Map([[key,raw]])});
 check(s.card.querySelector('.card-name').textContent==='Server default','noncanonical or unsafe numeric ID rejects whole map: '+id);
}
for(const url of ['https://example.test/'+ 'a'.repeat(2027),'https://example.test/?q=%F0%9F%98%80']) {
 const s=fixture('browser');click(s);s.elements['service-editor-url'].value=url;send(s);
 check(!s.dialog.open && s.requests.length===0,'URL2048-byte boundary or percent-encoded Unicode stays valid');
}
check(!/fetch\s*\(|XMLHttpRequest|csrf|revision/.test(browserSource),'browser adapter has no edit transport or shared tokens');
console.log(`service editor interaction checks passed: ${checks}`);
