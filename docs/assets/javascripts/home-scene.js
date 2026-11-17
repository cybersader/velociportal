/* Home-page illustration motion (documentation only).
   Four flat SVG planes follow normal document scroll: far +0.60H,
   middle +0.30H, stage 0 (portal, raptor and ground), near -0.12H, where H is
   the scene height and progress = clamp(frame-top scroll / H, 0, 1). The
   scene itself is not sticky and scrolls away. Without this script, with
   reduced motion, or if markup is missing, the static poster remains.
   A manual pause freezes current plane offsets and sprite pose; resuming
   re-aligns to current document progress. One requestAnimationFrame is queued
   only on demand, and only while the scene is on screen and the tab visible. */
(function () {
  'use strict';
  var doc = document;
  var win = window;
  var scene = doc.getElementById('vp-scene');
  if (!scene) return;
  var controls = doc.getElementById('vp-scene-controls');
  var button = doc.getElementById('vp-scene-motion');
  var label = button && button.querySelector('.vp-scene-motion__label');
  var planes = [
    doc.getElementById('vp-scene-far'),
    doc.getElementById('vp-scene-middle'),
    doc.getElementById('vp-scene-near')
  ];
  var factors = [0.6, 0.3, -0.12];
  var PX_PER_UNIT = 2; // 360-unit viewBox drawn at 720 CSS px.
  if (!controls || !button || !label || planes.indexOf(null) !== -1 ||
      typeof win.matchMedia !== 'function' ||
      typeof win.requestAnimationFrame !== 'function') return;

  var preference = win.matchMedia('(prefers-reduced-motion: reduce)');
  var paused = false;
  var frame = 0;
  var H = 260;
  var sceneTop = 0;
  var start = 0;
  var viewH = 0;

  function reduced() { return !!preference.matches; }
  function enabled() { return !paused && !reduced(); }
  function scrollY() { return win.pageYOffset || doc.documentElement.scrollTop || 0; }
  function visible() { var y = scrollY(); return y < sceneTop + H && y + viewH > sceneTop; }
  function cancel() { if (frame) { win.cancelAnimationFrame(frame); frame = 0; } }
  function poster() { planes.forEach(function (p) { p.removeAttribute('transform'); }); }

  function paint() {
    frame = 0;
    if (!enabled() || doc.hidden) return;
    var inView = visible();
    scene.setAttribute('data-running', String(inView));
    if (!inView) return;
    var progress = Math.max(0, Math.min(1, (scrollY() - start) / H));
    planes.forEach(function (plane, i) {
      plane.setAttribute('transform', 'translate(0 ' + Math.round(progress * H * factors[i]) / PX_PER_UNIT + ')');
    });
  }

  function queue() {
    if (!enabled() || doc.hidden) return;
    if (!visible()) { scene.setAttribute('data-running', 'false'); cancel(); return; }
    if (!frame) frame = win.requestAnimationFrame(paint);
  }

  function measure() {
    H = scene.offsetHeight || H;
    viewH = win.innerHeight || doc.documentElement.clientHeight || 0;
    sceneTop = scene.getBoundingClientRect().top + scrollY();
    // Progress starts when the frame first moves; for an initially visible
    // home scene that is document scroll position 0.
    start = Math.max(0, sceneTop - viewH);
    queue();
  }

  function sync() {
    cancel();
    var on = enabled();
    scene.setAttribute('data-motion', on ? 'on' : 'off');
    scene.setAttribute('data-running', String(on && !doc.hidden && visible()));
    button.disabled = reduced();
    button.setAttribute('aria-pressed', String(!on));
    label.textContent = reduced() ? 'Motion off: reduced motion' : 'Pause motion';
    if (on) queue();
    else if (reduced()) poster(); // A manual pause keeps current offsets.
  }

  button.addEventListener('click', function () { paused = !paused; sync(); });
  if (typeof preference.addEventListener === 'function') preference.addEventListener('change', sync);
  else if (typeof preference.addListener === 'function') preference.addListener(sync);
  win.addEventListener('scroll', queue, { passive: true });
  win.addEventListener('resize', measure, { passive: true });
  doc.addEventListener('visibilitychange', function () {
    if (doc.hidden) { cancel(); scene.setAttribute('data-running', 'false'); } else queue();
  });
  if (typeof win.ResizeObserver === 'function') new win.ResizeObserver(measure).observe(scene);
  controls.hidden = false;
  measure();
  sync();
})();
