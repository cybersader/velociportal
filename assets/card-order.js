// A browser preference, not a catalog: keep only typed IDs, never links or names.
(() => {
  'use strict';
  const el = id => document.getElementById(id);
  const root = () => el('portal-content');
  const scope = root()?.dataset.identityScope;
  const dialog = el('card-order'), opener = el('card-order-open'), list = el('card-order-list');
  if (!/^[a-f0-9]{64}$/.test(scope || '') || !dialog?.showModal || !opener || !list) return;
  const key = 'velociportal.card-order.v1.' + scope, limit = 2048;
  const valid = id => typeof id === 'string' && /^(?:npm:[1-9][0-9]{0,18}|ps_[a-f0-9]{32})$/.test(id);
  const idOf = node => node.dataset.personalService || 'npm:' + node.dataset.serviceId;
  const originals = new WeakMap();
  let signature = '', drag = null, reset = false, blocked = false;
  const validList = ids => Array.isArray(ids) && ids.length <= limit && ids.every(valid) && new Set(ids).size === ids.length;
  // The server (shared) order each grid had before this browser's order was applied.
  const serverOrder = gs => gs.flatMap(g => originals.get(g.grid) || []);
  // Saved as {ids, base}: base is the shared order at save time. When the shared
  // order later changes (for example an agent reorders the portal), the newer
  // shared order wins and this browser's copy is dropped. Added or removed
  // services alone do not count as a change.
  function write(ids, gs) { localStorage.setItem(key, JSON.stringify({v:2, ids, base:serverOrder(gs)})); }
  function read(gs) {
    const raw = localStorage.getItem(key);
    if (!raw || raw.length > 200000) return [];
    let saved; try { saved = JSON.parse(raw); } catch (_) { return []; }
    if (Array.isArray(saved)) {
      if (!validList(saved)) return [];
      try { write(saved, gs); } catch (_) { /* keep using the legacy copy */ }
      return saved;
    }
    if (!saved || saved.v !== 2 || !validList(saved.ids) || !validList(saved.base)) return [];
    const now = serverOrder(gs), was = new Set(saved.base), present = new Set(now);
    if (now.filter(id => was.has(id)).join() !== saved.base.filter(id => present.has(id)).join()) {
      try { localStorage.removeItem(key); } catch (_) { /* ignore */ }
      return [];
    }
    return saved.ids;
  }
  // Discover only currently rendered services; never reconstruct a card from storage.
  function groups() {
    if (blocked || root()?.dataset.identityScope !== scope) return [];
    const service = root().querySelector('#services');
    if (!service) return [];
    const grids = service.matches('.grid') ? [service] : [...service.querySelectorAll('.service-category > .grid')];
    return grids.map(grid => {
      const cards = [...grid.children].filter(n => n.hasAttribute('data-service') && valid(idOf(n)));
      if (!originals.has(grid)) originals.set(grid, cards.map(idOf));
      return {grid, cards, title: grid.parentElement.querySelector('.service-category-title')?.textContent || ''};
    });
  }
  const stamp = gs => JSON.stringify(gs.map(g => [g.title, ...g.cards.map(n => [idOf(n), n.dataset.service]).sort((a,b) => a[0].localeCompare(b[0]))]));
  function sorted(cards, ids) {
    const rank = new Map(ids.map((id,i) => [id,i]));
    return cards.slice().sort((a,b) => (rank.get(idOf(a)) ?? Infinity) - (rank.get(idOf(b)) ?? Infinity));
  }
  function refresh() {
    const gs = groups();
    if (dialog.open && stamp(gs) !== signature) dialog.close();
    let ids; try { ids = read(gs); } catch (_) { ids = []; }
    const all = gs.flatMap(g => g.cards.map(idOf));
    opener.hidden = !gs.some(g => g.cards.length > 1) || all.length > limit || new Set(all).size !== all.length;
    for (const {cards,grid} of gs) {
      const order = sorted(sorted(cards, originals.get(grid)), ids);
      if (order.some((n,i) => n !== cards[i])) grid.append(...order);
    }
  }
  function icon(path) {
    const svg = document.createElementNS('http://www.w3.org/2000/svg','svg');
    svg.setAttribute('width','16'); svg.setAttribute('height','16'); svg.setAttribute('viewBox','0 0 16 16'); svg.setAttribute('aria-hidden','true');
    const p = document.createElementNS(svg.namespaceURI,'path'); p.setAttribute('d',path); p.setAttribute('stroke','currentColor'); p.setAttribute('stroke-width','1.5'); p.setAttribute('stroke-linecap','round'); p.setAttribute('fill','none'); svg.append(p); return svg;
  }
  function positions(ol) {
    [...ol.children].forEach((row,i) => {
      row.querySelector('[data-step="-1"]').disabled = i === 0;
      row.querySelector('[data-step="1"]').disabled = i === ol.children.length-1;
    });
  }
  function render(gs, defaults) {
    list.replaceChildren();
    for (const {title,cards,grid} of gs) {
      if (title) { const h = document.createElement('h3'); h.textContent = title; list.append(h); }
      const ol = document.createElement('ol'); if (title) ol.setAttribute('aria-label',title); list.append(ol);
      for (const card of defaults ? sorted(cards, originals.get(grid)) : cards) {
        const row = document.createElement('li'); row.dataset.id = idOf(card); row.className = 'order-row';
        const grip = document.createElement('span'); grip.className = 'order-grip'; grip.setAttribute('aria-hidden','true'); grip.append(icon('M4 5h8M4 8h8M4 11h8'));
        const label = document.createElement('span'); label.className = 'order-name'; label.textContent = card.querySelector('.card-name')?.textContent || card.dataset.service;
        row.append(grip,label);
        for (const [step,name,path] of [[-1,'up','m4 10 4-4 4 4'],[1,'down','m4 6 4 4 4-4']]) {
          const b = document.createElement('button'); b.type = 'button'; b.dataset.step = step; b.setAttribute('aria-label','Move '+label.textContent+' '+name); b.append(icon(path)); row.append(b);
        }
        ol.append(row);
      }
      positions(ol);
    }
  }
  function announce(row) {
    const siblings = [...row.parentElement.children];
    el('card-order-status').textContent = row.querySelector('.order-name').textContent+': '+(siblings.indexOf(row)+1)+' of '+siblings.length;
  }
  opener.addEventListener('click', () => {
    refresh(); if (opener.hidden) return;
    const gs = groups(); signature = stamp(gs); reset = false; el('card-order-status').textContent = ''; render(gs,false); dialog.showModal();
  });
  list.addEventListener('click', e => {
    const b = e.target.closest('button[data-step]'); if (!b || b.disabled) return;
    const row = b.closest('li'), up = b.dataset.step === '-1', ol = row.parentElement;
    if (up) ol.insertBefore(row,row.previousElementSibling); else ol.insertBefore(row.nextElementSibling,row);
    positions(ol); (b.disabled ? row.querySelector('button:not(:disabled)') : b)?.focus(); reset = false; announce(row);
  });
  // Capture on the stationary viewport, not on the row that moves in the DOM.
  function drop() { if (!drag) return; cancelAnimationFrame(drag.frame); drag.row.removeAttribute('data-dragging'); drag = null; }
  function place() {
    const {x,y,row} = drag, r = list.getBoundingClientRect();
    if (y < r.top+32) list.scrollTop -= 6; else if (y > r.bottom-32) list.scrollTop += 6;
    const other = document.elementFromPoint(x,Math.max(r.top+1,Math.min(r.bottom-1,y)))?.closest('.order-row');
    if (other && other !== row && other.parentElement === row.parentElement) {
      const b = other.getBoundingClientRect(), after = y > b.top+b.height/2, next = after ? other.nextSibling : other;
      if (next !== row && row.nextSibling !== next) { row.parentElement.insertBefore(row,next); reset = false; }
    }
    drag.frame = requestAnimationFrame(place);
  }
  list.addEventListener('pointerdown', e => {
    if (drag || e.button !== 0 || !e.target.closest('.order-grip')) return;
    const row = e.target.closest('.order-row'); list.setPointerCapture(e.pointerId); row.setAttribute('data-dragging','');
    drag = {id:e.pointerId,row,x:e.clientX,y:e.clientY,frame:0}; drag.frame = requestAnimationFrame(place);
  });
  list.addEventListener('pointermove', e => { if (drag?.id === e.pointerId) { drag.x=e.clientX; drag.y=e.clientY; } });
  list.addEventListener('lostpointercapture', () => { if (drag) { positions(drag.row.parentElement); announce(drag.row); drop(); } });
  el('card-order-default').addEventListener('click', () => { drop(); render(groups(),true); reset=true; el('card-order-status').textContent='Default order selected. Save to apply.'; });
  el('card-order-cancel').addEventListener('click', () => dialog.close());
  dialog.addEventListener('close', () => { if (dialog.open) return; drop(); list.replaceChildren(); el('card-order-status').textContent=''; });
  el('card-order-save').addEventListener('click', () => {
    if (stamp(groups()) !== signature) { dialog.close(); return; }
    try {
      if (reset) localStorage.removeItem(key);
      else write([...list.querySelectorAll('.order-row')].map(n => n.dataset.id), groups());
    } catch (_) { el('card-order-status').textContent='Could not save in this browser. Your changes are still here.'; return; }
    dialog.close(); refresh();
  });
  // Drag a card on the page to move it within its group. A mouse or pen drag
  // starts after a few pixels of movement; touch needs a short press and hold
  // first so ordinary scrolling still works. A plain click still opens the link.
  const HOLD_MS = 350, SLOP = 8, MOUSE_START = 5;
  let live = null, swallowUntil = 0;
  function cardAt(x, y) {
    return document.elementsFromPoint(x, y).map(n => n.closest('[data-service]'))
      .find(n => n && n !== live.card && n.parentElement === live.grid) || null;
  }
  // Keep the lifted card under the pointer while its slot moves in the grid.
  function track() {
    const {card,x,y} = live, r = card.getBoundingClientRect();
    live.tx = x - live.gx - (r.left - live.tx); live.ty = y - live.gy - (r.top - live.ty);
    card.style.transform = `translate(${live.tx}px, ${live.ty}px)`;
  }
  function follow() {
    const {x,y,card,grid} = live, edge = 48, h = window.innerHeight;
    if (y < edge) window.scrollBy(0, -10); else if (y > h-edge) window.scrollBy(0, 10);
    const other = cardAt(x,y);
    if (other) grid.insertBefore(card, card.compareDocumentPosition(other) & Node.DOCUMENT_POSITION_FOLLOWING ? other.nextSibling : other);
    track();
    live.frame = requestAnimationFrame(follow);
  }
  function lift() {
    if (!live || dialog.open) return end(false);
    const r = live.card.getBoundingClientRect();
    Object.assign(live, {active:true, gx:live.x0-r.left, gy:live.y0-r.top, tx:0, ty:0});
    live.card.setAttribute('data-lifted',''); live.grid.setAttribute('data-sorting','');
    document.getSelection()?.removeAllRanges();
    if (live.touch) navigator.vibrate?.(10);
    track(); live.frame = requestAnimationFrame(follow);
  }
  function end(commit) {
    if (!live) return;
    const {card,grid,before,active,timer,frame} = live; live = null;
    clearTimeout(timer); cancelAnimationFrame(frame);
    if (!active) return;
    card.style.transform = ''; card.removeAttribute('data-lifted'); grid.removeAttribute('data-sorting');
    swallowUntil = Date.now() + 500;
    const now = [...grid.children];
    if (now.every((n,i) => n === before[i])) return;
    if (commit && root()?.dataset.identityScope === scope && !blocked) {
      try { const gs = groups(); write(gs.flatMap(g => g.cards.map(idOf)), gs); refresh(); return; }
      catch (_) { /* fall through and put the card back */ }
    }
    grid.append(...before);
  }
  const draggable = target => target.closest?.('#services [data-service]') && !target.closest('button, details, input, select, textarea');
  document.addEventListener('pointerdown', e => {
    if (live || dialog.open || e.button !== 0 || !e.isPrimary || !draggable(e.target)) return;
    const card = e.target.closest('[data-service]');
    const g = groups().find(g => g.grid === card.parentElement && g.cards.includes(card));
    if (!g || g.cards.length < 2) return;
    const touch = e.pointerType === 'touch';
    live = {card, grid:g.grid, id:e.pointerId, touch, x0:e.clientX, y0:e.clientY, x:e.clientX, y:e.clientY, active:false, before:[...g.grid.children], frame:0};
    if (touch) live.timer = setTimeout(lift, HOLD_MS);
  });
  document.addEventListener('pointermove', e => {
    if (!live || e.pointerId !== live.id) return;
    live.x = e.clientX; live.y = e.clientY;
    if (live.active) return;
    const moved = Math.hypot(live.x-live.x0, live.y-live.y0);
    if (live.touch) { if (moved > SLOP) end(false); }
    else if (moved > MOUSE_START) lift();
  });
  document.addEventListener('pointerup', e => { if (live?.id === e.pointerId) end(true); });
  document.addEventListener('pointercancel', e => { if (live?.id === e.pointerId) end(false); });
  // The browser's own link and image dragging would steal the gesture.
  document.addEventListener('dragstart', e => { if (draggable(e.target)) e.preventDefault(); });
  // Once lifted, keep the page from scrolling or opening a link menu.
  document.addEventListener('touchmove', e => { if (live?.active) e.preventDefault(); }, {passive:false});
  document.addEventListener('contextmenu', e => { if (live) e.preventDefault(); });
  document.addEventListener('click', e => {
    if (Date.now() < swallowUntil && e.target.closest?.('#services')) { e.preventDefault(); e.stopPropagation(); swallowUntil = 0; }
  }, true);
  document.addEventListener('keydown', e => { if (e.key === 'Escape' && live) end(false); });
  window.addEventListener('blur', () => end(false));
  document.body.addEventListener('htmx:beforeSwap', () => end(false));
  window.addEventListener('storage', e => { if (e.key === key || e.key === null) { if (dialog.open) dialog.close(); refresh(); } });
  document.body.addEventListener('htmx:afterSwap', refresh);
  document.body.addEventListener('htmx:responseError', e => {
    if (e.detail?.target?.id === 'portal-content' && [401,403].includes(e.detail.xhr?.status)) { blocked=true; refresh(); }
  });
  refresh();
})();
