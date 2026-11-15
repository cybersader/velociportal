#!/usr/bin/env node
// Actual shipped controller in a dependency-free DOM shim: state/race evidence,
// not native layout, focus trapping, accessibility-tree or two-device acceptance.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
const source = fs.readFileSync(new URL('../assets/personal-services.js', import.meta.url), 'utf8');
let checks=0;
function check(value,message) { assert.ok(value,message);checks++; }
const scope='a'.repeat(64), gen='b'.repeat(32), nextGen='c'.repeat(32), id='ps_'+'1'.repeat(32);
const entry=(name='My tool',more={})=>({id,name,url:'https://server.tailnet.ts.net/app',...more});
const state=(revision='0',entries=[],generation=gen)=>({generation,revision,initialized:revision!=='0',document:{version:1,entries},identity_scope:scope,csrf:'1234567890.'+'d'.repeat(64),icons:['generic','grafana'],projection:entries.map(e=>({id:e.id,status:'active'}))});
const storeID='e'.repeat(32), otherStore='f'.repeat(32);
const durableState=(revision='0',entries=[],generation=gen,store=storeID)=>({...state(revision,entries,generation),durable:true,store_id:store});
const copy=s=>JSON.stringify({version:s.durable?2:1,generation:s.generation,revision:s.revision,document:s.document,...(s.durable?{store_id:s.store_id}:{})});
const tick=()=>new Promise(r=>setImmediate(r));
async function settled(){await tick();await tick();}
class Element {
 constructor(id='',tag='div'){this.id=id;this.tagName=tag.toUpperCase();this.attrs={};this.listeners={};this.childNodes=[];this.hidden=false;this.disabled=false;this.value='';this.textContent='';this.isConnected=true;}
 getAttribute(k){return this.attrs[k]??null;} setAttribute(k,v){this.attrs[k]=String(v);}
 appendChild(e){e.parent=this;e.document=this.document;this.childNodes.push(e);return e;}
 replaceChildren(...nodes){this.childNodes=[];nodes.forEach(e=>this.appendChild(e));}
 contains(e){return !!e&&(e===this||this.childNodes.some(child=>child.contains(e)));}
 remove(){if(this.parent)this.parent.childNodes=this.parent.childNodes.filter(e=>e!==this);this.parent=null;this.isConnected=false;}
 addEventListener(k,f){(this.listeners[k]||=[]).push(f);}
 emit(k,event={}){event.target||=this;event.preventDefault||=()=>{event.prevented=true;};for(const f of this.listeners[k]||[])f(event);return event;}
 matches(s){const m=/^\[([^=\]]+)(?:="([^"]*)")?\]$/.exec(s);return m?this.attrs[m[1]]!==undefined&&(m[2]===undefined||this.attrs[m[1]]===m[2]):false;}
 closest(s){return (s==='[hidden]'?this.hidden:this.matches(s))?this:this.parent?.closest(s)||null;}
 querySelectorAll(s){return this.childNodes.flatMap(e=>[...(s.split(',').some(x=>e.matches(x.trim()))?[e]:[]),...e.querySelectorAll(s)]);}
 querySelector(s){return this.querySelectorAll(s)[0]||null;}
 focus(){this.document.activeElement=this;}
}
function fragmentAttrs(html){
 const tag=html.match(/<div\b[^>]*\bid="portal-content"[^>]*>/)?.[0];
 assert.ok(tag,'actual rendered portal root is present');
 return Object.fromEntries([...tag.matchAll(/(data-[a-z-]+)="([^"]*)"/g)].map(m=>[m[1],m[2]]));
}
function fixture(options={}){
 const ids=['portal-content','services-heading','portal-search-input','personal-editor','personal-manager','personal-editor-form','personal-editor-fields','personal-editor-name','personal-editor-url','personal-editor-icon','personal-editor-save','personal-editor-delete','personal-editor-reload','personal-editor-status','personal-announcement','personal-manager-status','personal-list','personal-recovery','personal-restore','personal-adopt','personal-refresh','personal-recovery-help','personal-recovery-list','personal-editor-cancel','personal-editor-preview','personal-editor-title'];
 const els=Object.fromEntries(ids.map(x=>[x,new Element(x)])),document=new Element('document');document.document=document;document.body=new Element('body');document.body.document=document;document.appendChild(document.body);
 for(const e of Object.values(els)){e.document=document;document.body.appendChild(e);}
 document.getElementById=id=>els[id]||null;document.createElement=tag=>{const e=new Element('',tag);e.document=document;return e;};
 const root=els['portal-content'];root.attrs={'data-identity-scope':scope,'data-personal-enabled':'true','data-personal-generation':gen,'data-personal-revision':options.revision||'0','data-personal-initialized':String(!!options.revision&&options.revision!=='0'),'data-service-edit-mode':'browser'};
 if(options.durable){root.attrs['data-personal-durable']='true';root.attrs['data-personal-store']=options.storeID||storeID;root.attrs['data-personal-owner-scope']='9'.repeat(64);}
 if(options.initialHTML)root.attrs=fragmentAttrs(options.initialHTML);
 const add=new Element('add','button');add.attrs['data-add-personal']='';add.document=document;root.appendChild(add);
 const select=els['personal-editor-icon'];select.options=['',...(options.icons||['generic','grafana'])].map(v=>{const e=new Element();e.value=v;e.attrs['data-icon-path']='/static/service-icons/'+(v||'generic')+(v==='grafana'?'.png':'.svg');return e;});
 const dialog=els['personal-editor'];dialog.showModal=()=>{dialog.open=true;};dialog.close=()=>{dialog.open=false;};
 els['personal-editor-form'].reset=()=>{for(const x of ['name','url','icon'])els['personal-editor-'+x].value='';};
 const requests=[],triggers=[],confirmations=[],storage=options.storage||new Map(),events=new Element('window'),timers=new Map();let timer=0;
 const s={els,document,root,add,dialog,requests,triggers,confirmations,storage,options,events,timers,confirm:true,now:2000000000000,tick:0,writes:0};
 s.advance=ms=>{s.now+=ms;s.tick+=ms;};
 class TestDate extends Date { static now(){return s.now;} }
 s.key='velociportal-personal-services-v1:http://portal.example:8081:'+root.attrs['data-identity-scope'];
 if(options.backup)storage.set(s.key,typeof options.backup==='string'?options.backup:copy(options.backup));
 const window={AbortController,performance:{now:()=>s.tick},location:{origin:'http://portal.example:8081'},confirm:text=>{confirmations.push(text);return s.confirm;},setTimeout:f=>{timers.set(++timer,f);return timer;},clearTimeout:t=>timers.delete(t),addEventListener:(k,f)=>events.addEventListener(k,f),htmx:{trigger:(el,n)=>triggers.push(n)},localStorage:{getItem:k=>{if(options.readError)throw new Error('blocked');return storage.get(k)??null;},setItem:(k,v)=>{if(options.saveError)throw new Error('quota');s.writes++;options.beforeWrite?.(k,v,s);storage.set(k,v);},removeItem:k=>{if(options.saveError)throw new Error('quota');storage.delete(k);}}};
 window.fetch=(path,opts)=>new Promise((resolve,reject)=>requests.push({path,opts,reject,resolve:(status,data,type='application/json')=>resolve({status,headers:{get:()=>type},text:async()=>typeof data==='string'?data:JSON.stringify(data)})}));
 class DOMParser{parseFromString(){return{getElementById:()=>s.incoming||root};}}
 vm.runInContext(source,vm.createContext({document,window,AbortController,TextEncoder,URL,DOMParser,console,Date:TestDate}));
 s.click=e=>document.emit('click',{target:e});s.send=()=>els['personal-editor-form'].emit('submit');s.edit=()=>s.click(els['personal-list'].querySelector('[data-edit-personal]'));
 s.swap=(attrs,html='fixture')=>{const incoming=new Element('portal-content');incoming.attrs=attrs;s.incoming=incoming;const event={detail:{target:root,xhr:{responseText:html},shouldSwap:true}};document.body.emit('htmx:beforeSwap',event);if(event.detail.shouldSwap){root.attrs={...incoming.attrs};if(html!=='fixture')root.replaceChildren();document.body.emit('htmx:afterSwap');}return event.detail.shouldSwap;};
 s.poll=(revision,generation=gen,extra={})=>s.swap({...root.attrs,'data-personal-revision':revision,'data-personal-generation':generation,'data-personal-initialized':String(revision!=='0'),...extra});
 s.fragment=html=>s.swap(fragmentAttrs(html),html);
 return s;
}
async function loaded(options={},value=state()){const s=fixture(options);s.requests[0].resolve(200,value);await settled();return s;}
function fill(s,name='My tool',url='https://server.tailnet.ts.net/app'){s.els['personal-editor-name'].value=name;s.els['personal-editor-url'].value=url;}
function body(s){return JSON.parse(s.requests.at(-1).opts.body);}
function msg(s){return s.els['personal-editor-status'].textContent;}
{
 const s=fixture();check(s.requests.length===1&&s.requests[0].opts.method==='GET','initial load makes one GET');check(s.add.disabled,'loading gates authoring');s.requests[0].resolve(200,state());await settled();check(!s.add.disabled&&!s.storage.has(s.key),'absent load does not create a recovery copy');s.click(s.add);fill(s);s.send();check(body(s).action==='create'&&body(s).revision==='0'&&body(s).csrf,'create uses loaded CAS/token');check(s.triggers.includes('htmx:abort')&&!s.poll('0'),'in-flight mutation suppresses old poll');check(!s.els['personal-announcement'].textContent,'no optimistic success');s.requests.at(-1).resolve(200,state('1',[entry()]));await settled();check(!s.dialog.open&&s.els['personal-announcement'].textContent==='Service saved.','acknowledged save closes');const saved=JSON.parse(s.storage.get(s.key));check(Object.keys(saved).sort().join(',')==='document,generation,revision,version'&&!/csrf|projection|identity_scope|icons/.test(s.storage.get(s.key)),'recovery copy contains authored records/bookkeeping only');check(s.triggers.includes('personal-services-saved'),'save requests ordinary server-rendered cards');check(s.root.querySelectorAll('[data-personal-service]').length===0,'controller never creates active cards');check(s.els['personal-list'].childNodes.length===1&&s.els['personal-list'].querySelectorAll('[href]').length===0,'manager has own editable record, never bypass URL');check(s.requests.every(r=>r.path==='/api/personal-services'),'no NPM editor or health transport');check(s.timers.size===0,'request deadlines cleaned');
}
{
 const s=await loaded({revision:'1'},state('1',[entry()]));s.edit();fill(s,'Unsaved draft');s.poll('2');check(s.requests.at(-1).opts.method==='GET','changed poll fetches full collection');s.requests.at(-1).resolve(200,state('2',[entry('Other device')]));await settled();check(s.els['personal-editor-name'].value==='Unsaved draft'&&s.els['personal-editor-save'].disabled,'remote revision retains and blocks dirty draft');check(JSON.parse(s.storage.get(s.key)).revision==='2','current acknowledged copy updates recovery');const count=s.requests.length;s.poll('2');check(s.requests.length===count,'unchanged poll has no extra GET');s.confirm=false;s.els['personal-editor-reload'].emit('click');check(s.requests.length===count,'review requires consent to replace dirty draft');s.confirm=true;s.els['personal-editor-reload'].emit('click');s.requests.at(-1).resolve(200,state('2',[entry('Other device')]));await settled();check(s.els['personal-editor-name'].value==='Other device'&&!s.els['personal-editor-save'].disabled,'explicit review reloads server values');check(!s.poll('1'),'older revision fragment suppressed');
}
{
 const s=await loaded({revision:'1'},state('1',[entry()]));s.edit();fill(s,'Draft');s.send();s.requests.at(-1).resolve(409,{error:'conflict'});await settled();check(s.dialog.open&&s.els['personal-editor-name'].value==='Draft'&&s.els['personal-editor-save'].disabled,'CAS conflict keeps draft and requires review');check(s.requests.length===2,'conflict does not auto replay or merge');
}
{
 const s=await loaded({revision:'1'},state('1',[entry()]));s.edit();s.els['personal-editor-delete'].emit('click');check(body(s).action==='delete'&&!Object.hasOwn(body(s),'name'),'delete sends only ID and CAS');s.requests.at(-1).resolve(200,state('2'));await settled();check(JSON.parse(s.storage.get(s.key)).document.entries.length===0,'initialized-empty deletion replaces same-generation backup');check(s.els['personal-restore'].hidden,'initialized empty never offers restore');
}
{
 const old=state('9',[entry()]);const s=await loaded({backup:old},state('0',[],nextGen));check(s.storage.get(s.key)===copy(old)&&!s.els['personal-restore'].hidden,'restart absence preserves prior generation and offers explicit restore');check(s.requests.length===1&&s.add.disabled,'no auto-seeding; editing waits for choice');s.els['personal-restore'].emit('click');check(body(s).generation===nextGen&&body(s).revision==='0'&&body(s).document.entries[0].id===id,'restore uses current CAS with prior authored document');s.requests.at(-1).resolve(200,state('1',[entry()],nextGen));await settled();check(JSON.parse(s.storage.get(s.key)).generation===nextGen&&s.els['personal-recovery'].hidden,'acknowledged explicit restore adopts current generation');check(s.els['personal-announcement'].textContent.includes('freshness cannot'),'restore does not claim newest recovery');
}
{
 const old=state('9',[entry()]);const s=await loaded({backup:old},state('2',[],nextGen));check(s.storage.get(s.key)===copy(old)&&s.els['personal-restore'].hidden&&!s.els['personal-adopt'].hidden,'initialized-empty after restart preserves old recovery but forbids resurrection');s.els['personal-adopt'].emit('click');check(JSON.parse(s.storage.get(s.key)).document.entries.length===0&&!s.add.disabled,'explicit adopt accepts initialized empty');check(s.requests.length===1,'adopt is local, no mutation');
 const a=await loaded({backup:old},state('0',[],nextGen));a.els['personal-adopt'].emit('click');check(!a.storage.has(a.key)&&!a.add.disabled,'explicit start empty removes old copy without seeding server');
}
{
 const old=state('9',[entry()]);const s=await loaded({backup:old},state('0',[],nextGen));s.els['personal-restore'].emit('click');s.requests.at(-1).resolve(409,{error:'conflict'});await settled();check(s.storage.get(s.key)===copy(old),'restore race preserves candidate');const n=s.requests.length;s.els['personal-restore'].emit('click');check(s.requests.length===n,'restore conflict cannot be blindly retried');s.els['personal-refresh'].emit('click');s.requests.at(-1).resolve(200,state('1',[entry('Already restored elsewhere')],nextGen));await settled();check(s.els['personal-restore'].hidden&&s.storage.get(s.key)===copy(old),'reload presents current collection and keeps old candidate until adoption');
}
{
 const s=await loaded({saveError:true});s.click(s.add);fill(s);s.send();s.requests.at(-1).resolve(200,state('1',[entry()]));await settled();check(!s.dialog.open&&s.els['personal-announcement'].textContent.includes('Saved on server')&&s.els['personal-announcement'].textContent.includes('unavailable'),'quota failure is server success, not false local success');check(!s.storage.has(s.key),'quota failure has no fake persisted copy');
 const corrupt=await loaded({backup:'{not json'},state('1',[entry()]));check(JSON.parse(corrupt.storage.get(corrupt.key)).document.entries[0].name==='My tool','corrupt copy is not consumed; confirmed server records can replace it');
 const blocked=await loaded({readError:true});check(!blocked.add.disabled,'blocked browser storage does not disable server authoring');
}
{
 for(const change of [{'data-identity-scope':'f'.repeat(64)},{'data-personal-enabled':'false'},{'data-service-edit-mode':'off'}]){
  const s=await loaded();s.click(s.add);fill(s,'Private draft');s.send();const pending=s.requests.at(-1);s.poll('0',gen,change);check(!s.dialog.open&&s.els['personal-editor-name'].value===''&&s.els['personal-manager'].hidden,'identity/feature/mode change closes and clears');pending.resolve(200,state('1',[entry()]));await settled();check(!s.storage.has(s.key)&&!s.els['personal-announcement'].textContent.includes('Service saved'),'delayed success after invalidation cannot store or announce');
 }
 const s=await loaded();s.click(s.add);fill(s);s.send();s.requests.at(-1).resolve(403,'forbidden','text/plain');await settled();check(!s.dialog.open&&s.els['personal-manager'].hidden,'plaintext identity denial invalidates before parsing');
}
{
 const s=await loaded();s.click(s.add);fill(s,'Draft');s.send();s.requests.at(-1).reject(new Error('network'));await settled();check(s.requests.length===3&&s.requests.at(-1).opts.method==='GET','uncertain POST gets exactly one bounded reconciliation read');s.requests.at(-1).resolve(200,state('1',[entry('Draft')]));await settled();check(s.dialog.open&&s.els['personal-editor-name'].value==='Draft'&&s.els['personal-editor-save'].disabled,'uncertain outcome keeps draft blocked for review');check(msg(s).includes('uncertain')&&!s.els['personal-announcement'].textContent.includes('saved'),'reconciled GET is not falsely called mutation confirmation');check(JSON.parse(s.storage.get(s.key)).revision==='1','acknowledged GET can preserve current records');check(s.requests.filter(r=>r.opts.method==='POST').length===1,'uncertain save never repeats mutation');
 const failed=await loaded();failed.click(failed.add);fill(failed);failed.send();failed.requests.at(-1).resolve(200,'<html>portal</html>','text/html');await settled();failed.requests.at(-1).reject(new Error('offline'));await settled();check(msg(failed).includes('Do not repeat')&&failed.requests.length===3,'HTML success treated uncertain, bounded GET failure does not loop');
}
{
 const s=await loaded({revision:'9007199254740993'},state('9007199254740993',[entry()]));s.edit();s.send();check(body(s).revision==='9007199254740993','revision above safe integer remains exact decimal string');s.requests.at(-1).resolve(200,state('9007199254740994',[entry()]));await settled();check(JSON.parse(s.storage.get(s.key)).revision==='9007199254740994','large revision preserved in backup');check(!s.poll('9007199254740993'),'decimal comparison suppresses old large revisions');
}
{
 const s=fixture();const stale=s.requests[0];s.poll('1');check(s.requests.length===2&&stale.opts.signal.aborted,'new poll supersedes initial delayed GET');stale.resolve(200,state());await settled();check(s.add.disabled,'old completion does not release current loading gate');s.requests[1].resolve(200,state('1',[entry()]));await settled();check(JSON.parse(s.storage.get(s.key)).revision==='1','latest GET owns state');
 s.poll('2');const old=s.requests.at(-1);s.poll('3');const latest=s.requests.at(-1);latest.resolve(200,state('3',[entry('Newest')]));await settled();old.resolve(200,state('2',[entry('Old')]));await settled();check(JSON.parse(s.storage.get(s.key)).revision==='3','late lower revision cannot overwrite newer backup');
 check(!s.poll('0',nextGen),'unknown generation fragment suppressed');s.requests.at(-1).resolve(200,state('0',[],nextGen));await settled();check(JSON.parse(s.storage.get(s.key)).generation===gen&&!s.els['personal-restore'].hidden,'generation change preserves old copy');const count=s.requests.length;check(!s.poll('3',gen)&&s.requests.length===count,'retired-generation fragment cannot reverse restart state');
}
{
 const s=await loaded({revision:'1'},state('1',[entry()]));s.edit();fill(s,'Local draft');s.storage.set(s.key,copy(state('2',[entry('Other tab')])));s.events.emit('storage',{key:s.key});check(s.els['personal-editor-name'].value==='Local draft'&&s.els['personal-editor-save'].disabled,'cross-tab change never replaces draft');check(s.requests.length===1,'storage event never writes server');s.poll('2');s.requests.at(-1).resolve(200,state('1',[entry()]));await settled();check(JSON.parse(s.storage.get(s.key)).revision==='2','delayed read cannot regress newer tab backup');
}
{
 const bad=[state('01'),{...state(),revision:0},{...state(),initialized:true},{...state('1',[entry()]),csrf:'bad'},state('1',[entry('Bad',{url:'javascript:alert(1)'})]),state('1',[entry('Bad',{icon:'../../evil'})])];
 for(const value of bad){const s=await loaded({},value);check(s.add.disabled&&!s.storage.has(s.key),'malformed API state rejected');}
 const s=fixture();s.requests[0].resolve(200,'<html>disabled route</html>','text/html');await settled();check(s.add.disabled&&!s.els['personal-refresh'].hidden,'disabled catch-all HTML cannot count as API success');
}
{
 const invalidURLs=['https://server.tailnet.ts.net/?token=x','https://server.tailnet.ts.net/#code','http://server.tailnet.ts.net','https://SERVER.tailnet.ts.net','https://server.tailnet.ts.net:443','https://server.tailnet.ts.net:00080','https://server.tailnet.ts.net:65536','https://user@server.tailnet.ts.net','https://server.tailnet.ts.net/a/../b','https://server.tailnet.ts.net/a%2Fb','https://server.tailnet.ts.net/%61','https://server.tailnet.ts.net/%C0%AF','https://server.tailnet.ts.net//a','https://server.tailnet.ts.net/%E2%80%8B','https://server.ts.net','https://100.64.0.1'];
 const s=await loaded();s.click(s.add);
 for(const u of invalidURLs){fill(s,'Tool',u);s.send();check(s.requests.length===1,'unsafe/noncanonical URL never sent: '+u);}
 for(const n of ['', ' padded', 'x'.repeat(121),'bad\ud800','bad\u0001']){fill(s,n);s.send();check(s.requests.length===1,'invalid name never sent');}
 fill(s,'界'.repeat(120),'https://server.tailnet.ts.net:8443/app');s.send();check(s.requests.length===2,'120 codepoints and canonical explicit nondefault port accepted');
}
{
 const s=await loaded({revision:'1'},state('1',[entry()]));const edit=s.els['personal-list'].querySelector('[data-edit-personal]');edit.focus();s.poll('1');check(s.document.activeElement===s.els['personal-list'].querySelector('[data-edit-personal]'),'unchanged poll preserves focused manager action');
 const active=new Element('active','article');active.attrs['data-personal-service']=id;s.root.appendChild(active);s.document.body.emit('htmx:responseError',{detail:{target:s.root,xhr:{status:401}}});check(s.root.querySelectorAll('[data-personal-service]').length===0&&!s.els['personal-recovery-help'].textContent,'identity denial removes stale personal cards and recovery details');
}
{
 const s=await loaded();s.click(s.add);fill(s);s.send();const post=s.requests.at(-1);[...s.timers.values()][0]();check(post.opts.signal.aborted,'POST deadline aborts network wait');post.reject(Object.assign(new Error('timeout'),{name:'AbortError'}));await settled();check(s.requests.length===3&&s.requests.at(-1).opts.method==='GET','POST timeout reconciles once, without mutation retry');s.requests.at(-1).resolve(200,state());await settled();check(s.dialog.open&&s.els['personal-editor-save'].disabled&&msg(s).includes('uncertain'),'same revision after timeout is not proof the mutation failed');
 const mismatched=await loaded();mismatched.click(mismatched.add);fill(mismatched);mismatched.send();mismatched.requests.at(-1).resolve(200,state('1',[entry('Not the submitted name')]));await settled();check(mismatched.requests.length===3&&!mismatched.els['personal-announcement'].textContent,'mismatched full-state acknowledgment is not false success');
}
{
 const tooMany=Array.from({length:129},(_,i)=>entry('Tool',{id:'ps_'+i.toString(16).padStart(32,'0')}));
 const tooLarge=Array.from({length:40},(_,i)=>entry('Tool',{id:'ps_'+i.toString(16).padStart(32,'0'),url:'https://server.tailnet.ts.net/'+('a'.repeat(1900))}));
 for(const entries of [tooMany,tooLarge,[entry(),entry()], [entry('broken'+String.fromCharCode(0xd800))]]){
  const s=await loaded({backup:copy(state('9',entries))});check(!s.storage.has(s.key)||s.storage.get(s.key)===copy(state('9',entries)),'invalid recovery copy is never rewritten as valid');check(!s.add.disabled&&s.els['personal-recovery'].hidden,'oversize/duplicate/malformed recovery records cannot seed recovery UI');
 }
 const old=state('9',[entry()]),s=await loaded({backup:old},state('0',[],nextGen));s.els['personal-restore'].emit('click');const other=state('12',[entry('Different browser copy')],'e'.repeat(32));s.storage.set(s.key,copy(other));s.events.emit('storage',{key:s.key});s.requests.at(-1).resolve(200,state('1',[entry()],nextGen));await settled();check(s.storage.get(s.key)===copy(other),'restore acknowledgment cannot discard a different candidate written meanwhile');check(!s.els['personal-recovery'].hidden,'later recovery candidate needs its own explicit adoption');
}
check(!/setInterval|serviceWorker|indexedDB|sessionStorage/.test(source),'no second timer/poll subsystem or alternate durable state');
console.log(`PASS ${checks} existing personal-service synthetic behavior checks (not native-browser proof)`);
// Preserved independent-review regressions, plus bounded corrective coverage.
// Collect all failures rather than hiding either original reproduction.
const regressions=[];
async function regression(name,run){try{await run();console.log(`PASS regression: ${name}`);}catch(error){regressions.push({name,message:error.message});console.error(`FAIL regression: ${name}: ${error.message}`);}}
await regression('unchanged catalog must not lose a draft when its initial CSRF expires',async()=>{
 const issued=2000000000,token=`${issued}.`+'d'.repeat(64);
 const s=await loaded({revision:'1'},{...state('1',[entry()]),csrf:token});
 // Thirty-one unchanged one-minute polls still must not create another poller.
 for(let minute=1;minute<=31;minute++){s.advance(60000);s.poll('1');}
 assert.equal(s.requests.length,1,'idle unchanged polls do not fetch extra tokens');
 s.edit();fill(s,'Draft after 31 minutes');s.send();
 const preflight=s.requests.at(-1);
 assert.equal(preflight.opts.method,'GET','aging token is refreshed before any mutation');
 assert.equal(s.requests.filter(r=>r.opts.method==='POST').length,0,'no expired-token POST was sent');
 assert.equal(s.els['personal-editor-name'].value,'Draft after 31 minutes','preflight retains draft');
 preflight.resolve(200,{...state('1',[entry()]),csrf:`${issued+31*60}.`+'d'.repeat(64)});await settled();
 const post=s.requests.findLast(r=>r.opts.method==='POST');
 assert.ok(post,'save attempt must issue a POST');
 const expired=issued+31*60-Number(JSON.parse(post.opts.body).csrf.split('.')[0])>30*60;
 if(expired)post.resolve(403,'Forbidden','text/plain');
 else post.resolve(200,{...state('2',[entry('Draft after 31 minutes')]),csrf:`${issued+31*60}.`+'d'.repeat(64)});
 await settled();
 assert.ok(!expired || s.dialog.open&&s.els['personal-editor-name'].value==='Draft after 31 minutes',`expired=${expired}, dialogOpen=${!!s.dialog.open}, draft=${JSON.stringify(s.els['personal-editor-name'].value)}; token expiry must refresh safely or retain the unsaved draft`);
 assert.equal(expired,false,'mutation uses refreshed token');
 assert.equal(s.els['personal-announcement'].textContent,'Service saved.');
 assert.equal(s.requests.filter(r=>r.opts.method==='POST').length,1,'no POST replay');
});
await regression('interleaved cross-tab backup writes must retain the higher acknowledged revision',async()=>{
 const newer=copy(state('3',[entry('Newer acknowledged catalog')]));let injected=false;
 const s=await loaded({revision:'1',beforeWrite:(key,value,tab)=>{
  if(!injected&&JSON.parse(value).revision==='2'){
   injected=true;
   // Another independent tab completes its acknowledged rev3 write after this
   // tab's final read/check, before this tab's localStorage.setItem(rev2).
   tab.storage.set(key,newer);
  }
 }},state('1',[entry()]));
 s.poll('2');s.requests.at(-1).resolve(200,state('2',[entry('Older acknowledged catalog')]));await settled();
 assert.ok(injected,'exercise the read/check/set interleaving, not newer-disk-before-accept');
 // Its delayed storage event carries rev3, but current disk already regressed.
 s.events.emit('storage',{key:s.key,newValue:newer});
 assert.equal(JSON.parse(s.storage.get(s.key)).revision,'3','rev3 was acknowledged between rev2 read/check and write; delayed event must not discard the higher authored recovery copy');
});
await regression('an already-open idle draft uses fresh CSRF without changing its CAS base',async()=>{
 const s=await loaded({revision:'1'},state('1',[entry()]));s.edit();fill(s,'Open idle draft');
 for(let i=0;i<31;i++){s.advance(60000);s.poll('1');}
 s.send();assert.equal(s.requests.at(-1).opts.method,'GET');assert.equal(s.els['personal-editor-name'].value,'Open idle draft');
 const csrf='2000001860.'+'e'.repeat(64);s.requests.at(-1).resolve(200,{...state('1',[entry()]),csrf});await settled();
 assert.equal(body(s).csrf,csrf);assert.equal(body(s).revision,'1');assert.equal(body(s).name,'Open idle draft');
 s.requests.at(-1).resolve(200,state('2',[entry('Open idle draft')]));await settled();assert.equal(s.els['personal-announcement'].textContent,'Service saved.');
});
await regression('preflight concurrent revision or generation changes require explicit draft review',async()=>{
 for(const changed of [state('2',[entry('Other client')]),state('0',[],nextGen)]){
  const s=await loaded({revision:'1'},state('1',[entry()]));s.edit();fill(s,'Keep this draft');s.advance(31*60000);s.send();
  s.requests.at(-1).resolve(200,changed);await settled();
  assert.equal(s.requests.filter(r=>r.opts.method==='POST').length,0,'never rebase and overwrite a concurrent change');
  assert.equal(s.els['personal-editor-name'].value,'Keep this draft');assert.ok(s.dialog.open&&s.els['personal-editor-save'].disabled&&!s.els['personal-editor-reload'].hidden);
 }
});
await regression('preflight failure retains draft without uncertain-save retry; real denials still invalidate',async()=>{
 for(const failure of ['network','busy','forbidden']){
  const s=await loaded({revision:'1'},state('1',[entry()]));s.edit();fill(s,'Preflight draft');s.advance(31*60000);s.send();
  if(failure==='network')s.requests.at(-1).reject(new Error('offline'));
  else if(failure==='busy')s.requests.at(-1).resolve(503,{error:'busy'});
  else s.requests.at(-1).resolve(403,'Forbidden','text/plain');
  await settled();assert.equal(s.requests.length,2,'one preflight, no extra reconciliation or replay');assert.equal(s.requests.filter(r=>r.opts.method==='POST').length,0);
  if(failure==='forbidden')assert.ok(!s.dialog.open&&s.els['personal-editor-name'].value===''&&s.els['personal-manager'].hidden);
  else assert.ok(s.dialog.open&&s.els['personal-editor-name'].value==='Preflight draft'&&msg(s).includes('Not saved.'));
 }
});
await regression('either elapsed clock detects token aging or backward clock drift',async()=>{
 for(const clock of ['wall','monotonic','backward']){
  const s=await loaded();s.click(s.add);fill(s);
  if(clock==='wall')s.now+=31*60000;else if(clock==='monotonic')s.tick+=31*60000;else s.now-=1000;
  s.send();assert.equal(s.requests.at(-1).opts.method,'GET');assert.equal(s.requests.length,2);
 }
});
await regression('retained acknowledged state repairs lower disk and delayed events without write ping-pong',async()=>{
 const s=await loaded({revision:'3'},state('3',[entry('Newest')]));s.edit();fill(s,'Dirty draft');
 const older=copy(state('2',[entry('Older')])),newer=copy(state('3',[entry('Newest')]));s.storage.set(s.key,older);
 const writes=s.writes;s.events.emit('storage',{key:s.key,newValue:older});assert.equal(JSON.parse(s.storage.get(s.key)).revision,'3');assert.equal(s.writes,writes+1);
 for(let i=0;i<20;i++)s.events.emit('storage',{key:s.key,newValue:i%2?newer:older});
 assert.equal(s.writes,writes+1,'equal/lower delayed events never echo writes');assert.equal(s.els['personal-editor-name'].value,'Dirty draft');assert.ok(s.els['personal-editor-save'].disabled);
 assert.equal(s.requests.length,1,'repair never writes to or fetches from server');assert.equal(s.root.querySelectorAll('[data-personal-service]').length,0,'repair never authorizes cards');
});
await regression('same-generation repair preserves initialized-empty deletion',async()=>{
 const s=await loaded({revision:'3'},state('3'));const older=copy(state('2',[entry()]));s.storage.set(s.key,older);s.events.emit('storage',{key:s.key,newValue:older});
 assert.equal(JSON.parse(s.storage.get(s.key)).revision,'3');assert.deepEqual(JSON.parse(s.storage.get(s.key)).document.entries,[]);assert.ok(s.els['personal-restore'].hidden);
});
await regression('repair does not rewrite prior-generation candidates or resurrect explicit clearing',async()=>{
 const old=state('9',[entry()]),s=await loaded({backup:old},state('0',[],nextGen));const before=s.storage.get(s.key),writes=s.writes;
 s.events.emit('storage',{key:s.key,newValue:copy(state('10',[entry('Prior run newer copy')]))});
 s.events.emit('storage',{key:s.key,newValue:copy(state('99',[entry('Unrelated run')],'f'.repeat(32)))});
 assert.equal(s.storage.get(s.key),before);assert.equal(s.writes,writes);assert.equal(s.requests.length,1);
 s.els['personal-adopt'].emit('click');assert.ok(!s.storage.has(s.key));s.events.emit('storage',{key:s.key,newValue:before});assert.ok(!s.storage.has(s.key),'delayed event cannot recreate a deliberately cleared copy');
});
await regression('event validation, storage failures and identity fencing remain fail-closed',async()=>{
 const s=await loaded({revision:'3'},state('3',[entry('Newest')]));const old=copy(state('2',[entry('Old')]));const writes=s.writes;
 s.events.emit('storage',{key:s.key,newValue:'{"version":1,"document":"hostile"}'});assert.equal(s.writes,writes,'malformed event does not supply a repair');
 s.storage.set(s.key,old);s.options.saveError=true;s.events.emit('storage',{key:s.key,newValue:old});assert.equal(JSON.parse(s.storage.get(s.key)).revision,'2');assert.ok(s.els['personal-manager-status'].textContent.includes('unavailable'),'failed repair is truthful and nonfatal');
 s.options.saveError=false;s.events.emit('storage',{key:s.key,newValue:old});assert.equal(JSON.parse(s.storage.get(s.key)).revision,'3','retained high copy survives quota failure');
 s.poll('3',gen,{'data-identity-scope':'f'.repeat(64)});s.storage.set(s.key,old);s.events.emit('storage',{key:s.key,newValue:copy(state('4',[entry('Late')]))});assert.equal(s.storage.get(s.key),old,'invalidated identity cannot repair or consume late events');
});
await regression('durable restart loads disk without copy choice and never rebases dirty drafts',async()=>{
 const old=durableState('3',[entry()]);const s=await loaded({durable:true,backup:old},durableState('3',[entry()],nextGen));
 assert.ok(!s.add.disabled&&s.els['personal-recovery'].hidden&&s.els['personal-restore'].hidden);assert.equal(s.requests.length,1);
 assert.equal(JSON.parse(s.storage.get(s.key)).version,2);assert.equal(JSON.parse(s.storage.get(s.key)).generation,nextGen);
 s.edit();fill(s,'Keep draft');s.poll('3','d'.repeat(32));s.requests.at(-1).resolve(200,durableState('3',[entry()],'d'.repeat(32)));await settled();
 assert.equal(s.els['personal-editor-name'].value,'Keep draft');assert.ok(s.els['personal-editor-save'].disabled);s.send();assert.equal(s.requests.filter(r=>r.opts.method==='POST').length,0);
 s.els['personal-editor-reload'].emit('click');s.requests.at(-1).resolve(200,durableState('3',[entry()],'d'.repeat(32)));await settled();s.send();assert.equal(body(s).store_id,storeID);assert.equal(body(s).generation,'d'.repeat(32));
});
await regression('durable conflicting legacy/store/rollback copies remain reviewable without blocking server data',async()=>{
 for(const old of [state('9',[entry('Legacy')]),durableState('9',[entry('Rollback')]),durableState('2',[entry('Different store')],gen,otherStore),durableState('2',[entry('Divergent')]),durableState('1',[entry('Older divergent')])]){
  const s=await loaded({durable:true,revision:'2',backup:old},durableState('2',[],nextGen));
  assert.equal(s.storage.get(s.key),copy(old));assert.ok(!s.add.disabled&&!s.els['personal-recovery'].hidden&&s.els['personal-restore'].hidden);
  assert.match(s.els['personal-recovery-help'].textContent,/not a known-newer/);assert.equal(s.els['personal-recovery-list'].childNodes[0].textContent,old.document.entries[0].name+' — '+old.document.entries[0].url);assert.equal(s.els['personal-recovery-list'].querySelectorAll('[href]').length,0);
  s.els['personal-adopt'].emit('click');assert.equal(JSON.parse(s.storage.get(s.key)).document.entries.length,0);assert.equal(s.requests.length,1);assert.ok(s.els['personal-recovery'].hidden);
 }
});
await regression('durable legacy import is explicit and absent-only; durable copies never auto-import',async()=>{
 const old=state('7',[entry()]);const s=await loaded({durable:true,backup:old},durableState('0',[],nextGen));assert.ok(!s.add.disabled&&!s.els['personal-restore'].hidden);assert.equal(s.requests.length,1);
 s.els['personal-restore'].emit('click');assert.equal(body(s).action,'restore');assert.equal(body(s).store_id,storeID);assert.equal(body(s).revision,'0');
 s.requests.at(-1).resolve(200,durableState('1',[entry()],nextGen));await settled();assert.equal(JSON.parse(s.storage.get(s.key)).version,2);assert.ok(s.els['personal-recovery'].hidden);
 const d=await loaded({durable:true,backup:durableState('7',[entry()])},durableState('0',[],nextGen));assert.ok(d.els['personal-restore'].hidden&&!d.add.disabled);assert.equal(d.requests.length,1);
});
await regression('durable uncertainty and unavailable responses never reconfirm stale data or replay',async()=>{
 for(const error of ['uncertain','unavailable']){
  const s=await loaded({durable:true,revision:'1'},durableState('1',[entry()]));const saved=s.storage.get(s.key),writes=s.writes;s.edit();fill(s,'Preserved draft');s.send();s.requests.at(-1).resolve(503,{error});await settled();
  assert.equal(s.storage.get(s.key),saved);assert.equal(s.writes,writes);assert.equal(s.els['personal-editor-name'].value,'Preserved draft');assert.ok(s.add.disabled&&s.els['personal-editor-save'].disabled);assert.equal(s.els['personal-list'].childNodes.length,0);assert.equal(s.requests.length,2);assert.doesNotMatch(msg(s),/Not saved/);
  s.els['personal-refresh'].emit('click');s.requests.at(-1).resolve(503,{error:'unavailable'});await settled();assert.equal(s.writes,writes);assert.equal(s.requests.filter(r=>r.opts.method==='POST').length,1);
 }
});
await regression('durable lost POST refreshes once without replay and keeps draft for review',async()=>{
 const s=await loaded({durable:true,revision:'1'},durableState('1',[entry()]));s.edit();fill(s,'Pending');s.send();s.requests.at(-1).reject(new Error('lost'));await settled();assert.equal(s.requests.at(-1).opts.method,'GET');s.requests.at(-1).resolve(200,durableState('2',[entry('Pending')]));await settled();
 assert.equal(s.requests.filter(r=>r.opts.method==='POST').length,1);assert.equal(s.els['personal-editor-name'].value,'Pending');assert.ok(s.els['personal-editor-save'].disabled);assert.match(msg(s),/uncertain/);
});
await regression('durable CSRF refresh does not adopt changed process/store CAS or dirty fields',async()=>{
 for(const changed of [durableState('1',[entry()],nextGen),durableState('1',[entry()],gen,otherStore)]){
  const s=await loaded({durable:true,revision:'1'},durableState('1',[entry()]));s.edit();fill(s,'Keep fields');s.advance(31*60000);s.send();s.requests.at(-1).resolve(200,changed);await settled();
  assert.equal(s.requests.filter(r=>r.opts.method==='POST').length,0);assert.equal(s.els['personal-editor-name'].value,'Keep fields');assert.ok(s.els['personal-editor-save'].disabled);
 }
});
await regression('durable strict wire rejects missing/unknown mode or store bookkeeping',async()=>{
 for(const value of [state(),{...durableState(),store_id:''},{...durableState(),store_id:null},{...durableState(),durable:false},{...durableState(),user_id:'not-browser-authority'},{...durableState(),namespace:'private'}]){
  const s=await loaded({durable:true},value);assert.ok(s.add.disabled);assert.ok(!s.storage.has(s.key));
 }
 const legacy=await loaded({},durableState());assert.ok(legacy.add.disabled);
 const s=await loaded({durable:true,revision:'1'},durableState('1',[entry()]));s.edit();fill(s,'Draft');s.poll('1',gen,{'data-personal-durable':'false'});assert.ok(!s.dialog.open&&s.els['personal-manager'].hidden);
});
await regression('durable competing copy survives a save and cross-tab events do not rewrite it',async()=>{
 const old=state('7',[entry('Legacy conflict')]);const s=await loaded({durable:true,revision:'1',backup:old},durableState('1',[entry()]));s.edit();fill(s,'Server update');s.send();s.requests.at(-1).resolve(200,durableState('2',[entry('Server update')]));await settled();
 assert.equal(s.storage.get(s.key),copy(old));assert.match(s.els['personal-announcement'].textContent,/retained for review/);
 const other=durableState('12',[entry('Other store')],nextGen,otherStore);s.storage.set(s.key,copy(other));const writes=s.writes;s.events.emit('storage',{key:s.key,newValue:copy(other)});assert.equal(s.writes,writes);assert.equal(s.storage.get(s.key),copy(other));assert.ok(!s.add.disabled&&!s.els['personal-recovery'].hidden);
});
await regression('durable final CSRF expiry keeps draft without retry; real denial still clears',async()=>{
 for(const code of ['csrf_expired','forbidden']){
  const s=await loaded({durable:true,revision:'1'},durableState('1',[entry()]));s.edit();fill(s,'Slow save draft');const old=s.storage.get(s.key);s.send();s.requests.at(-1).resolve(403,{error:code});await settled();
  assert.equal(s.requests.filter(r=>r.opts.method==='POST').length,1);assert.equal(s.storage.get(s.key),old);
  if(code==='csrf_expired'){assert.equal(s.els['personal-editor-name'].value,'Slow save draft');assert.ok(s.dialog.open&&s.els['personal-editor-save'].disabled);assert.match(msg(s),/expired/);}
  else assert.ok(!s.dialog.open&&s.els['personal-manager'].hidden);
 }
});
await regression('durable equal-revision divergence on disk is never silently overwritten',async()=>{
 const s=await loaded({durable:true,revision:'2'},durableState('2',[entry('Server')]));const competing=durableState('2',[entry('Divergent browser')]);s.storage.set(s.key,copy(competing));s.events.emit('storage',{key:s.key,newValue:copy(competing)});
 s.els['personal-refresh'].emit('click');s.requests.at(-1).resolve(200,durableState('2',[entry('Server')]));await settled();assert.equal(s.storage.get(s.key),copy(competing));assert.ok(!s.els['personal-recovery'].hidden&&!s.add.disabled);
});
await regression('durable displaced event evidence survives check/write interleaving without restoring or rebasing',async()=>{
 for(const candidate of [durableState('3',[entry('Displaced newer')]),durableState('9',[entry('Displaced different store')],nextGen,otherStore),state('7',[entry('Displaced legacy')]),durableState('3',[]),durableState('2',[entry('Equal revision divergent')])]){
  const displaced=copy(candidate);let injected=false;
  const s=await loaded({durable:true,revision:'1',beforeWrite:(key,value,tab)=>{if(!injected&&JSON.parse(value).revision==='2'){injected=true;tab.storage.set(key,displaced);}}},durableState('1',[entry()]));
  s.poll('2');s.requests.at(-1).resolve(200,durableState('2',[entry('Current server')]));await settled();assert.ok(injected);
  const stored=s.storage.get(s.key),writes=s.writes,requests=s.requests.length;
  s.events.emit('storage',{key:s.key,newValue:displaced});
  assert.equal(s.storage.get(s.key),stored,'event is review evidence, not disk-write authority');assert.equal(s.writes,writes);assert.equal(s.requests.length,requests);
  assert.ok(!s.els['personal-recovery'].hidden&&!s.add.disabled&&s.els['personal-restore'].hidden);
  assert.ok(s.els['personal-recovery-list'].childNodes.some(n=>n.textContent.includes(candidate.document.entries[0]?.name||'Initialized-empty')));
  assert.equal(s.els['personal-recovery-list'].querySelectorAll('[href]').length,0);assert.match(s.els['personal-recovery-help'].textContent,/only in this tab/);
  s.edit();fill(s,'Current CAS draft');s.events.emit('storage',{key:s.key,newValue:displaced});assert.equal(s.els['personal-editor-name'].value,'Current CAS draft');assert.ok(s.els['personal-editor-save'].disabled);
  s.els['personal-editor-reload'].emit('click');s.requests.at(-1).resolve(200,durableState('2',[entry('Current server')]));await settled();s.send();
  assert.equal(body(s).revision,'2');assert.equal(body(s).store_id,storeID);assert.equal(body(s).generation,gen);
 }
});
await regression('durable API-before-storage-event ordering retains the previously reviewed conflict',async()=>{
 for(const candidate of [state('7',[entry('Legacy reviewed copy')]),durableState('9',[entry('Other store reviewed copy')],nextGen,otherStore),durableState('8',[])]){
  const current=durableState('2',[entry('Current server')]),conflict=copy(candidate),replacement=copy(current);
  const s=await loaded({durable:true,revision:'2',backup:candidate},current);
  assert.ok(!s.els['personal-recovery'].hidden);s.edit();fill(s,'Keep dirty fields');
  s.storage.set(s.key,replacement); // Other tab's event has NOT run.
  s.els['personal-refresh'].emit('click');s.requests.at(-1).resolve(200,current);await settled();
  assert.ok(!s.els['personal-recovery'].hidden,'API observation alone must retain conflicting C');
  const rows=()=>s.els['personal-recovery-list'].childNodes.map(n=>n.textContent);
  const expected=candidate.document.entries[0]?.name||'Initialized-empty';assert.ok(rows().some(t=>t.includes(expected)));
  const writes=s.writes,requests=s.requests.length;
  s.events.emit('storage',{key:s.key,oldValue:conflict,newValue:replacement});
  assert.ok(rows().some(t=>t.includes(expected)),'delivered D event cannot erase C');assert.equal(s.storage.get(s.key),replacement);assert.equal(s.writes,writes);assert.equal(s.requests.length,requests);
  assert.equal(s.els['personal-editor-name'].value,'Keep dirty fields');assert.ok(s.els['personal-editor-save'].disabled);assert.ok(s.els['personal-restore'].hidden);
  s.els['personal-editor-reload'].emit('click');s.requests.at(-1).resolve(200,current);await settled();s.send();assert.equal(body(s).revision,'2');assert.equal(body(s).store_id,storeID);assert.equal(body(s).generation,gen);
 }
});
await regression('durable displaced candidates remain bounded and require explicit discard',async()=>{
 const s=await loaded({durable:true,revision:'2'},durableState('2',[]));const writes=s.writes;
 for(let i=0;i<6;i++)s.events.emit('storage',{key:s.key,newValue:copy(durableState(String(i+3),[entry('Candidate '+i)],nextGen,otherStore))});
 assert.equal(s.writes,writes);assert.equal(s.els['personal-recovery-list'].childNodes.length,4);assert.match(s.els['personal-recovery-help'].textContent,/four-copy review limit/);assert.ok(s.els['personal-restore'].hidden);
 s.events.emit('storage',{key:s.key,newValue:'{"version":2,"document":"invalid"}'});assert.equal(s.els['personal-recovery-list'].childNodes.length,4);
 s.confirm=false;s.els['personal-adopt'].emit('click');assert.equal(s.els['personal-recovery-list'].childNodes.length,4);
 s.confirm=true;s.options.saveError=true;s.els['personal-adopt'].emit('click');assert.equal(s.els['personal-recovery-list'].childNodes.length,4,'failed discard write keeps evidence');
 s.options.saveError=false;s.els['personal-adopt'].emit('click');assert.ok(s.els['personal-recovery'].hidden);assert.equal(s.els['personal-recovery-list'].childNodes.length,0);assert.equal(s.requests.length,1);
});
await regression('durable uncertainty poll blocks delayed API completion and real owner changes still clear',async()=>{
 const s=await loaded({durable:true,revision:'1'},durableState('1',[entry()]));s.edit();fill(s,'Retained through poll');s.send();const pending=s.requests.at(-1),stored=s.storage.get(s.key),writes=s.writes;
 s.poll('0',gen,{'data-personal-unavailable':'true'});assert.ok(pending.opts.signal.aborted&&s.dialog.open&&s.els['personal-editor-save'].disabled);
 pending.resolve(200,durableState('2',[entry('Retained through poll')]));await settled();assert.equal(s.storage.get(s.key),stored);assert.equal(s.writes,writes);assert.equal(s.requests.length,2);
 assert.equal(s.els['personal-editor-name'].value,'Retained through poll');assert.equal(s.els['personal-list'].childNodes.length,0);
 s.poll('1',nextGen,{'data-personal-unavailable':'false'});s.requests.at(-1).resolve(200,durableState('1',[entry()],nextGen));await settled();assert.ok(s.dialog.open&&s.els['personal-editor-save'].disabled);assert.equal(s.els['personal-editor-name'].value,'Retained through poll');
 s.poll('1',nextGen,{'data-personal-owner-scope':'8'.repeat(64)});assert.ok(!s.dialog.open&&s.els['personal-manager'].hidden);assert.equal(s.els['personal-editor-name'].value,'');
});
// S3: artwork belongs to rendered cards, never authored form/recovery state.
function displayedPersonalIcon(s, record, iconID) {
 const card=new Element('personal-card','article');card.attrs['data-personal-service']=record.id;
 const image=new Element('shared-icon','img');image.attrs.src='/static/service-icons/'+iconID+(iconID==='generic'?'.svg':'.png');card.appendChild(image);s.root.appendChild(card);
 return image;
}
await regression('S3 empty icon previews inheritance without claiming generic or changing authored state',async()=>{
 for(const durable of [false,true]){
  const make=durable?durableState:state,record=entry(),initial=make('1',[record]);
  const s=await loaded({durable,revision:'1'},initial),image=displayedPersonalIcon(s,record,'grafana');
  const stored=s.storage.get(s.key),writes=s.writes;s.edit();
  assert.equal(s.els['personal-editor-icon'].value,'');assert.equal(s.els['personal-editor-preview'].hidden,true);
  s.els['personal-editor-icon'].value='generic';s.els['personal-editor-icon'].emit('change');
  assert.equal(s.els['personal-editor-preview'].hidden,false);assert.equal(s.els['personal-editor-preview'].src,'/static/service-icons/generic.svg');
  s.els['personal-editor-icon'].value='';s.els['personal-editor-icon'].emit('change');
  assert.equal(s.els['personal-editor-preview'].hidden,true);assert.equal(s.els['personal-editor-icon'].value,'');
  image.attrs.src='/static/service-icons/immich.png';s.poll('1');
  assert.equal(s.els['personal-editor-icon'].value,'');assert.equal(s.els['personal-editor-preview'].hidden,true);
  assert.equal(s.requests.length,1,'shared default refresh cannot cause a personal mutation or extra API read');
  assert.equal(s.storage.get(s.key),stored);assert.equal(s.writes,writes,'preview/poll cannot write recovery data');
 }
});
await regression('S3 create and update send only authored empty or explicit icon and retain that in recovery',async()=>{
 for(const durable of [false,true]){
  const make=durable?durableState:state,s=await loaded({durable},make());
  s.click(s.add);fill(s);assert.equal(s.els['personal-editor-preview'].hidden,true);s.send();
  assert.equal(body(s).action,'create');assert.equal(body(s).icon,'');
  s.requests.at(-1).resolve(200,make('1',[entry()]));await settled();
  assert.deepEqual(JSON.parse(s.storage.get(s.key)).document,make('1',[entry()]).document);
  displayedPersonalIcon(s,entry(),'grafana');s.edit();
  s.els['personal-editor-icon'].value='generic';s.els['personal-editor-icon'].emit('change');s.send();
  assert.equal(body(s).action,'update');assert.equal(body(s).icon,'generic');
  assert.deepEqual(Object.keys(body(s)).sort(),['action','csrf','generation','icon','id','name','revision','url',...(durable?['store_id']:[])].sort());
  s.requests.at(-1).resolve(200,make('2',[entry('My tool',{icon:'generic'})]));await settled();
  assert.equal(JSON.parse(s.storage.get(s.key)).document.entries[0].icon,'generic');
  s.edit();assert.equal(s.els['personal-editor-icon'].value,'generic');assert.equal(s.els['personal-editor-preview'].hidden,false);
  s.els['personal-editor-icon'].value='';s.els['personal-editor-icon'].emit('change');s.send();
  assert.equal(body(s).icon,'','blank removes the personal override rather than saving the displayed shared icon');
  s.requests.at(-1).resolve(200,make('3',[entry()]));await settled();
  const backup=JSON.parse(s.storage.get(s.key));assert.deepEqual(backup.document,make('3',[entry()]).document);
  assert.doesNotMatch(s.storage.get(s.key),/grafana|immich|target_digest|effective_icon|projection|csrf/);
  assert.equal(s.requests.filter(r=>r.opts.method==='POST').length,3,'one POST per explicit authored operation');
  const reloaded=await loaded({durable,revision:'3',storage:s.storage},make('3',[entry()]));reloaded.edit();
  assert.equal(reloaded.els['personal-editor-icon'].value,'');assert.equal(reloaded.els['personal-editor-preview'].hidden,true);
 }
});
await regression('S3 legacy restore and durable import preserve omitted empty and explicit generic icons exactly',async()=>{
 const records=[entry('Omitted'),entry('Empty',{id:'ps_'+'2'.repeat(32),icon:''}),entry('Explicit generic',{id:'ps_'+'3'.repeat(32),icon:'generic'})];
 const legacy=state('7',records);
 for(const durable of [false,true]){
  const make=durable?durableState:state,s=await loaded({durable,backup:legacy},make('0',[],nextGen));
  displayedPersonalIcon(s,records[0],'grafana');assert.equal(s.requests.length,1,'no automatic restore');
  s.els['personal-restore'].emit('click');assert.equal(body(s).action,'restore');assert.deepEqual(body(s).document,legacy.document);
  assert.ok(!Object.hasOwn(body(s).document.entries[0],'icon'));assert.equal(body(s).document.entries[1].icon,'');assert.equal(body(s).document.entries[2].icon,'generic');
  assert.ok(!Object.hasOwn(body(s),'icon'),'restore carries a document, not a resolved card icon');
  s.requests.at(-1).resolve(200,make('1',records,nextGen));await settled();
  const stored=JSON.parse(s.storage.get(s.key));assert.deepEqual(stored.document,legacy.document);assert.equal(stored.version,durable?2:1);
  assert.doesNotMatch(s.storage.get(s.key),/grafana|target_digest|effective_icon/);assert.equal(s.requests.filter(r=>r.opts.method==='POST').length,1);
 }
});
await regression('S3 authored wire and browser copies reject shared-default and digest fields',async()=>{
 for(const extra of [{effective_icon:'grafana'},{target_digest:'sha256:'+'a'.repeat(64)}]){
  const malformed=entry('Malformed',extra),s=await loaded({},state('1',[malformed]));
  assert.ok(s.add.disabled);assert.ok(!s.storage.has(s.key),'untrusted projection data cannot become an authored recovery copy');
  const badCopy=copy(state('7',[malformed])),recovery=await loaded({backup:badCopy},state('0',[],nextGen));
  assert.equal(recovery.storage.get(recovery.key),badCopy,'invalid stored data is not rewritten');assert.ok(recovery.els['personal-recovery'].hidden);assert.equal(recovery.requests.length,1);
 }
});
if(process.env.VELOCIPORTAL_SHARED_ICON_FIXTURE){
 await regression('S3 actual Go default-only poll keeps authored form API requests and browser copy separate',async()=>{
  const f=JSON.parse(fs.readFileSync(process.env.VELOCIPORTAL_SHARED_ICON_FIXTURE,'utf8'));
  assert.deepEqual(f.state.document,f.refreshed_state.document);assert.equal(f.state.revision,f.refreshed_state.revision);
  assert.ok(!f.state.document.entries[0].icon);assert.match(f.initial,/Inherit shared default \(otherwise generic\)/);
  assert.match(f.initial,/<img id="personal-editor-preview"[^>]* hidden>/);
  const s=await loaded({initialHTML:f.initial,icons:f.state.icons},f.state),record=f.state.document.entries[0];
  const stored=s.storage.get(s.key),writes=s.writes;s.edit();assert.equal(s.els['personal-editor-icon'].value,'');assert.equal(s.els['personal-editor-preview'].hidden,true);
  assert.ok(s.fragment(f.refreshed));assert.equal(s.els['personal-editor-icon'].value,'');assert.equal(s.els['personal-editor-preview'].hidden,true);
  assert.equal(s.storage.get(s.key),stored);assert.equal(s.writes,writes);assert.equal(s.requests.length,1);
  s.send();assert.equal(body(s).icon,'');assert.equal(body(s).name,record.name);assert.equal(body(s).url,record.url);assert.equal(body(s).revision,f.state.revision);
  const acknowledged={...f.refreshed_state,revision:String(BigInt(f.state.revision)+1n)};
  s.requests.at(-1).resolve(200,acknowledged);await settled();
  assert.equal(s.dialog.open,false);assert.deepEqual(JSON.parse(s.storage.get(s.key)).document,f.state.document);
  assert.doesNotMatch(s.storage.get(s.key),/immich|grafana|target_digest|effective_icon/);assert.equal(s.requests.filter(r=>r.opts.method==='POST').length,1);
 });
}else console.log('SKIP S3 actual shared-icon poll fixture (set VELOCIPORTAL_SHARED_ICON_FIXTURE to generated Go artifact)');
if(process.env.VELOCIPORTAL_PERSONAL_POLL_FIXTURE){
 await regression('actual uncertain API response through production portal fragment preserves blocked draft',async()=>{
  const f=JSON.parse(fs.readFileSync(process.env.VELOCIPORTAL_PERSONAL_POLL_FIXTURE,'utf8'));
  for(const [kind,fragment] of Object.entries(f.invalid)){
   const s=await loaded({durable:true,initialHTML:f.healthy,icons:f.state.icons},f.state);s.edit();fill(s,'Uncertain local draft');s.send();
   const saved=s.storage.get(s.key),writes=s.writes;s.requests.at(-1).resolve(503,f.response);await settled();
   const card=new Element('stale-card');card.attrs['data-personal-service']=f.state.document.entries[0].id;s.root.appendChild(card);
   assert.ok(s.fragment(f.uncertain));assert.ok(s.dialog.open&&s.els['personal-editor-save'].disabled);assert.equal(s.els['personal-editor-name'].value,'Uncertain local draft');assert.equal(s.root.querySelectorAll('[data-personal-service]').length,0);assert.equal(s.els['personal-list'].childNodes.length,0);
   assert.equal(s.storage.get(s.key),saved);assert.equal(s.writes,writes);assert.equal(s.requests.length,2,'poll neither loads unavailable state nor replays');
   s.fragment(f.uncertain);assert.equal(s.els['personal-editor-name'].value,'Uncertain local draft');
   s.fragment(fragment);assert.ok(!s.dialog.open&&s.els['personal-manager'].hidden,kind+' must invalidate');assert.equal(s.els['personal-editor-name'].value,'');assert.equal(s.requests.filter(r=>r.opts.method==='POST').length,1);
  }
 });
}else console.log('SKIP production uncertain-poll fixture (set VELOCIPORTAL_PERSONAL_POLL_FIXTURE to generated Go artifact)');
if(regressions.length){console.error(JSON.stringify({regression_failures:regressions},null,2));process.exitCode=1;}
else console.log('PASS independent-review regressions');
