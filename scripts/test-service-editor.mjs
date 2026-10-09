#!/usr/bin/env node
// Dependency-free synthetic interaction tests of the actual shipped controller.
// A DOM shim checks state/races, not layout, native focus trapping or real keyboards.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
const source = fs.readFileSync(new URL('../assets/service-editor.js', import.meta.url), 'utf8');
let checks = 0;
function check(value, message) { assert.ok(value, message); checks++; }
class Element {
  constructor(id) { this.id = id; this.attrs = {}; this.listeners = {}; this.value = ''; this.textContent = ''; this.hidden = false; this.disabled = false; this.isConnected = true; }
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
function fixture() {
  const ids = ['service-editor','service-editor-form','service-editor-fields','service-editor-name','service-editor-url','service-editor-icon','service-editor-preview','service-editor-status','service-editor-save','service-editor-reset','service-editor-reload','service-editor-cancel','service-editor-announcement','service-editor-defaults','service-editor-title','portal-content','services-heading','portal-search-input'];
  const elements = Object.fromEntries(ids.map(id => [id,new Element(id)]));
  const document = new Element('document'); document.body = new Element('body'); document.getElementById = id => elements[id] || null;
  const root = elements['portal-content']; root.attrs = {'data-identity-scope':'alpha','data-service-editing':'true'};
  const button = new Element('edit-3'); button.attrs['data-edit-service']='3'; root.cards=[button];
  root.querySelector = selector => root.cards.find(b => selector === '[data-edit-service="'+b.attrs['data-edit-service']+'"]') || null;
  root.querySelectorAll = () => root.cards;
  document.querySelectorAll = () => root.cards;
  for (const e of [...Object.values(elements),button]) e.document=document;
  const icon = elements['service-editor-icon']; icon.options = ['','generic','grafana'].map(v=>{const e=new Element(v); e.attrs['data-icon-path']='/static/service-icons/'+(v || 'generic')+(v==='grafana'?'.png':'.svg'); return e;});
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
  const context = vm.createContext({document,window,fetch,AbortController,DOMParser,console});
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
check(!/localStorage|sessionStorage|indexedDB/.test(source),'controller stores no drafts/tokens');
console.log(`service editor interaction checks passed: ${checks}`);
