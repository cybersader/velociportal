#!/usr/bin/env node
// Dependency-free checks of the documentation home-scene controller and its
// static markup. A fake DOM checks state, not native layout, painting,
// CSS animation timing, real scrolling or assistive technology.
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import fs from 'node:fs';
import vm from 'node:vm';

const read = (path) => fs.readFileSync(new URL(path, import.meta.url), 'utf8');
const source = read('../docs/assets/javascripts/home-scene.js');
const partial = read('../overrides/partials/vp-home-scene.html');
const template = read('../overrides/home.html');
const css = read('../docs/assets/stylesheets/extra.css');
const index = read('../docs/index.md');
const mkdocs = read('../mkdocs.yml');
let checks = 0;
function check(value, message) { assert.ok(value, message); checks++; }
function equal(actual, expected, message) { assert.deepEqual(actual, expected, message); checks++; }

class Node {
  constructor(id) { this.id = id; this.attrs = {}; this.listeners = {}; this.hidden = false; this.disabled = false; this.textContent = ''; }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return this.attrs[k] ?? null; }
  removeAttribute(k) { delete this.attrs[k]; }
  addEventListener(k, f) { (this.listeners[k] ||= []).push(f); }
  emit(k) { for (const f of this.listeners[k] || []) f({}); }
}

function fixture({ reduced = false, scrollY = 0, sceneTop = 20, height = 260, viewH = 800, omit = [], noRaf = false } = {}) {
  const ids = ['vp-scene', 'vp-scene-controls', 'vp-scene-motion', 'vp-scene-far', 'vp-scene-middle', 'vp-scene-near'];
  const el = Object.fromEntries(ids.filter((id) => !omit.includes(id)).map((id) => [id, new Node(id)]));
  const label = new Node('label');
  if (el['vp-scene-controls']) el['vp-scene-controls'].hidden = true;
  if (el['vp-scene-motion']) el['vp-scene-motion'].querySelector = (s) => (s === '.vp-scene-motion__label' ? label : null);
  const scene = el['vp-scene'];
  const state = { scrollY, frames: new Map(), next: 1, requested: 0 };
  if (scene) {
    scene.attrs = { 'data-motion': 'off', 'data-running': 'false' };
    scene.offsetHeight = height;
    scene.getBoundingClientRect = () => ({ top: sceneTop - state.scrollY });
  }
  const preference = new Node('mq'); preference.matches = reduced;
  const document = new Node('document');
  document.hidden = false;
  // Deliberately enormous: progress must not depend on article/document height.
  document.documentElement = { scrollTop: 0, clientHeight: viewH, scrollHeight: 40000 };
  document.getElementById = (id) => el[id] || null;
  const window = new Node('window');
  Object.defineProperty(window, 'pageYOffset', { get: () => state.scrollY });
  window.innerHeight = viewH;
  window.matchMedia = () => preference;
  if (!noRaf) {
    window.requestAnimationFrame = (fn) => { state.requested++; const id = state.next++; state.frames.set(id, fn); return id; };
    window.cancelAnimationFrame = (id) => { state.frames.delete(id); };
  }
  vm.runInNewContext(source, { window, document });
  const flush = () => { const fns = [...state.frames.values()]; state.frames.clear(); fns.forEach((f) => f()); };
  const scroll = (y) => { state.scrollY = y; window.emit('scroll'); };
  const offsets = () => ['vp-scene-far', 'vp-scene-middle', 'vp-scene-near'].map((id) => el[id]?.getAttribute('transform') ?? null);
  return { el, label, scene, preference, document, window, state, flush, scroll, offsets };
}

// Syntax.
new vm.Script(source); checks++;

// Missing markup: no throw, nothing enabled, controls stay hidden.
fixture({ omit: ['vp-scene'] }); checks++;
for (const missing of ['vp-scene-middle', 'vp-scene-motion', 'vp-scene-controls']) {
  const f = fixture({ omit: [missing] });
  equal(f.state.requested, 0, `missing ${missing}: no animation frame`);
  equal(f.scene.getAttribute('data-motion'), 'off', `missing ${missing}: static poster`);
  if (f.el['vp-scene-controls']) check(f.el['vp-scene-controls'].hidden, `missing ${missing}: toggle stays hidden`);
}
{
  const f = fixture({ noRaf: true });
  check(f.el['vp-scene-controls'].hidden && f.scene.getAttribute('data-motion') === 'off', 'no requestAnimationFrame: static poster');
}

// Default (motion allowed): toggle shown, motion on, document-scroll progress.
{
  const f = fixture({ scrollY: 100 });
  check(!f.el['vp-scene-controls'].hidden, 'toggle revealed by script');
  const b = f.el['vp-scene-motion'];
  equal([b.getAttribute('aria-pressed'), b.disabled, f.label.textContent], ['false', false, 'Pause motion'], 'stable name, not pressed');
  equal(f.scene.getAttribute('data-motion'), 'on', 'motion on');
  equal(f.state.frames.size, 1, 'one frame queued on demand');
  f.flush();
  // progress = 100/260 of H=260: far 60px, middle 30px, near -12px; SVG units are px/2.
  equal(f.offsets(), ['translate(0 30)', 'translate(0 15)', 'translate(0 -6)'], 'factors +.60/+.30/-.12 of document scroll');
  equal(f.scene.getAttribute('data-running'), 'true', 'sprite runs while visible');
  f.scroll(130); f.scroll(140); f.scroll(150);
  equal(f.state.frames.size, 1, 'scroll bursts coalesce into one frame');
  f.flush();
  equal(f.offsets(), ['translate(0 45)', 'translate(0 22.5)', 'translate(0 -9)'], 'follows scroll');
  f.scroll(60); f.flush();
  equal(f.offsets(), ['translate(0 18)', 'translate(0 9)', 'translate(0 -3.5)'], 'reverses exactly with scroll, no smoothing');
  f.scroll(270); f.flush();
  equal(f.offsets(), ['translate(0 78)', 'translate(0 39)', 'translate(0 -15.5)'], 'progress clamps at 1');
  f.scroll(0); f.flush();
  equal(f.offsets(), ['translate(0 0)', 'translate(0 0)', 'translate(0 0)'], 'progress 0 at top');

  // Offscreen: frame exits normally; no frame, sprite stopped.
  f.scroll(400);
  equal([f.state.frames.size, f.scene.getAttribute('data-running')], [0, 'false'], 'offscreen pauses work');
  f.scroll(100); f.flush();
  equal([f.scene.getAttribute('data-running'), f.offsets()[0]], ['true', 'translate(0 30)'], 'returns when visible');

  // Hidden document.
  f.document.hidden = true; f.document.emit('visibilitychange');
  equal(f.scene.getAttribute('data-running'), 'false', 'hidden tab stops sprite');
  f.scroll(150);
  equal(f.state.frames.size, 0, 'hidden tab queues nothing');
  f.document.hidden = false; f.document.emit('visibilitychange'); f.flush();
  equal(f.offsets()[0], 'translate(0 45)', 'visible tab re-aligns');

  // Manual pause freezes current offsets and pose; resume re-aligns.
  b.emit('click');
  equal([b.getAttribute('aria-pressed'), f.label.textContent, f.scene.getAttribute('data-motion'), f.scene.getAttribute('data-running')],
    ['true', 'Pause motion', 'off', 'false'], 'pause: pressed, same name, sprite paused');
  const frozen = f.offsets();
  equal(frozen[0], 'translate(0 45)', 'pause does not reset to poster');
  f.scroll(200); f.flush();
  equal([f.offsets(), f.state.frames.size], [frozen, 0], 'paused scroll leaves offsets frozen');
  b.emit('click'); f.flush();
  equal([b.getAttribute('aria-pressed'), f.offsets()[0]], ['false', 'translate(0 60)'], 'resume re-aligns to current progress');

  // Reduced motion turned on later: static poster and disabled control.
  f.preference.matches = true; f.preference.emit('change');
  equal([f.offsets(), b.disabled, b.getAttribute('aria-pressed'), f.scene.getAttribute('data-motion')],
    [[null, null, null], true, 'true', 'off'], 'reduced motion: poster, disabled, truthful state');
  f.scroll(120); equal(f.state.frames.size, 0, 'reduced motion queues nothing');
  f.preference.matches = false; f.preference.emit('change'); f.flush();
  equal(f.offsets()[0], 'translate(0 36)', 'reduced motion off re-aligns');
}

// Reduced motion at load: static default.
{
  const f = fixture({ reduced: true, scrollY: 100 });
  equal([f.state.requested, f.offsets(), f.scene.getAttribute('data-motion')], [0, [null, null, null], 'off'], 'reduced default is static');
  equal([f.el['vp-scene-motion'].disabled, f.label.textContent], [true, 'Motion off: reduced motion'], 'truthful disabled control');
}

// A frame below the fold starts its progress when it first enters.
{
  const f = fixture({ sceneTop: 1200, viewH: 800, scrollY: 530 });
  f.flush();
  equal(f.offsets()[0], 'translate(0 39)', 'progress from first frame movement into view, not document height');
}

// Mobile height uses H=220.
{
  const f = fixture({ height: 220, scrollY: 110 });
  f.flush();
  equal(f.offsets(), ['translate(0 33)', 'translate(0 16.5)', 'translate(0 -6.5)'], 'H follows rendered scene height');
}

// Static markup and integration contract.
check(/id="vp-scene-controls" hidden/.test(partial), 'toggle hidden without JS');
check(/class="vp-scene" id="vp-scene" data-motion="off" data-running="false"/.test(partial), 'static poster without JS');
check(/role="img" aria-labelledby="vp-scene-title vp-scene-desc"/.test(partial), 'accessible image name');
check(!/aria-label=/.test(partial.split('<svg')[0] + partial.slice(partial.lastIndexOf('</svg>'))), 'toggle name comes from visible text');
check(!/<script|<image|href=|url\(|https?:/.test(partial.replace('http://www.w3.org/2000/svg', '')), 'no remote or scripted art');
check(!/[{][{]|[{]%/.test(partial.replace(/^[{]#-[\s\S]*?-#[}]/, '')), 'no template syntax inside art');
const geometry = [...partial.matchAll(/(?<=\s)(?:fill|d|opacity|transform|viewBox|width|height|shape-rendering)="[^"]*"/g)]
  .map((m) => m[0]).filter((a) => !/^(?:width|height)="1[02]"$|viewBox="0 0 12 12"/.test(a) && !a.startsWith('d="M2 1'));
equal(crypto.createHash('sha256').update(geometry.join('')).digest('hex'),
  'eefd3b0531e9b3e7129a282a88e2727f565e9dfbfb8f443646384ef9d553aa05', 'accepted artwork geometry and palette unchanged');
check(/extends "main\.html"/.test(template) && /[{][{] super\(\) [}][}]/.test(template), 'template inherits Material shell');
check(/assets\/javascripts\/home-scene\.js/.test(template), 'home-only script');
check(/^---\ntemplate: home\.html\n---\n/.test(index), 'home page selects template');
check(!/pixel-field-guide|pixel-raptor/.test(index + mkdocs + css), 'old home art removed');
check(/^  logo: assets\/logo-card\.svg$/m.test(mkdocs) && /^  favicon: assets\/logo-card\.svg$/m.test(mkdocs), 'original logo and favicon');
check(/^  custom_dir: overrides$/m.test(mkdocs), 'override directory configured');
check(!/^extra_javascript:/m.test(mkdocs), 'script not loaded site-wide');
const sceneCss = css.slice(css.indexOf('/* Home illustration'), css.indexOf('.md-typeset .vp-intro {')).replace(/\/\*[\s\S]*?\*\//g, '');
check(sceneCss.length > 0 && !/sticky|fixed|overflow-y|overflow:\s*auto|scroll/.test(sceneCss), 'scene in normal flow, no nested scroll');
check(!/transition/.test(sceneCss), 'no transition smoothing on planes');
check(/prefers-reduced-motion: reduce[\s\S]*transform: none !important/.test(sceneCss), 'reduced-motion CSS backstop');
check(/height: 260px/.test(sceneCss) && /height: 220px/.test(sceneCss), 'accepted 260/220 frame heights');
check(/\.vp-scene\[data-motion="on"\]\[data-running="true"\] \.pose \{ animation-play-state: running; \}/.test(sceneCss), 'sprite runs only when on and visible');
check(!/\.md-logo[^{]*\{[^}]*filter/.test(css), 'header logo not filtered');

console.log(`PASS: ${checks} home-scene checks (fake DOM + static source; not a native-browser proof).`);
