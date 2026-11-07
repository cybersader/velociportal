/* Personal authored records only. Server-rendered cards remain the only links.
 * One GET on load/change, CAS writes, explicit restart recovery; no outbound queue. */
(function () {
  "use strict";
  var byID = function (id) { return document.getElementById(id); };
  var root = function () { return byID("portal-content"); };
  var dialog = byID("personal-editor"), manager = byID("personal-manager");
  if (!dialog || !manager || typeof dialog.showModal !== "function" || !window.AbortController || typeof TextEncoder !== "function" || !window.fetch) return;
  var initial = root(), scope = initial && initial.getAttribute("data-identity-scope");
  if (!/^[a-f0-9]{64}$/.test(scope || "") || initial.getAttribute("data-personal-enabled") !== "true") return;
  var mode = initial.getAttribute("data-service-edit-mode");
  var key = "velociportal-personal-services-v1:" + window.location.origin + ":" + scope;
  var form = byID("personal-editor-form"), fields = byID("personal-editor-fields");
  var name = byID("personal-editor-name"), url = byID("personal-editor-url"), icon = byID("personal-editor-icon");
  var save = byID("personal-editor-save"), remove = byID("personal-editor-delete"), reload = byID("personal-editor-reload");
  var status = byID("personal-editor-status"), notice = byID("personal-announcement"), summary = byID("personal-manager-status");
  var list = byID("personal-list"), recovery = byID("personal-recovery"), restore = byID("personal-restore"), adopt = byID("personal-adopt"), refresh = byID("personal-refresh");
  var icons = new Map();
  Array.from(icon.options).forEach(function (option) {
    var path = option.getAttribute("data-icon-path");
    if (/^\/static\/service-icons\/[a-z0-9-]+\.(svg|png)$/.test(path || "")) icons.set(option.value, path);
  });
  var state = null, backup = null, storageProblem = "", invalid = false, posting = false, loading = false;
  var draft = null, epoch = 0, controller = null, retired = new Set(), loadingHint = "";
  var recoveryChoice = false, tokenReceivedAt = null, tokenReceivedTick = null;
  function monotonicNow() { return window.performance && typeof window.performance.now === "function" ? window.performance.now() : Date.now(); }
  function tokenNeedsRefresh() {
    var wallAge = Date.now() - tokenReceivedAt, tickAge = monotonicNow() - tokenReceivedTick;
    // Fresh server responses mint a token. Leave five minutes of its 30-minute
    // lifetime for request delays; either clock detects idle/sleep or clock drift.
    return tokenReceivedAt === null || wallAge < 0 || tickAge < 0 || Math.max(wallAge, tickAge) >= 25 * 60 * 1000;
  }
  var maxRevision = "18446744073709551615";
  function object(value) { return value !== null && typeof value === "object" && !Array.isArray(value); }
  function keys(value, required, optional) {
    return object(value) && required.every(function (k) { return Object.hasOwn(value, k); }) && Object.keys(value).every(function (k) { return required.includes(k) || (optional || []).includes(k); });
  }
  function compare(a, b) { return a.length !== b.length ? (a.length < b.length ? -1 : 1) : (a === b ? 0 : a < b ? -1 : 1); }
  function revision(value) { return typeof value === "string" && /^(0|[1-9][0-9]{0,19})$/.test(value) && compare(value, maxRevision) <= 0; }
  function nextRevision(value) {
    var digits = value.split(""), carry = true;
    for (var i = digits.length - 1; i >= 0 && carry; i--) {
      if (digits[i] === "9") digits[i] = "0";
      else { digits[i] = String.fromCharCode(digits[i].charCodeAt(0) + 1); carry = false; }
    }
    return (carry ? "1" : "") + digits.join("");
  }
  function sameDocument(a, b) {
    function normalized(d) { return d.entries.map(function (e) { return [e.id, e.name, e.url, e.icon || ""]; }); }
    return JSON.stringify(normalized(a)) === JSON.stringify(normalized(b));
  }
  function confirms(result, body, previous) {
    if (result.generation !== body.generation || result.revision !== nextRevision(body.revision) || !result.initialized) return false;
    if (body.action === "restore") return sameDocument(result.document, body.document);
    var entries = previous.entries.map(function (e) { return Object.assign({}, e); });
    if (body.action === "create") {
      var added = result.document.entries[result.document.entries.length - 1];
      if (!added || entries.some(function (e) { return e.id === added.id; })) return false;
      entries.push({ id: added.id, name: body.name, url: body.url, icon: body.icon });
    } else if (body.action === "delete") entries = entries.filter(function (e) { return e.id !== body.id; });
    else entries = entries.map(function (e) { return e.id === body.id ? { id: e.id, name: body.name, url: body.url, icon: body.icon } : e; });
    return sameDocument(result.document, { entries: entries });
  }
  function generation(value) { return typeof value === "string" && /^[a-f0-9]{32}$/.test(value); }
  function personalID(value) { return typeof value === "string" && /^ps_[a-f0-9]{32}$/.test(value); }
  function clean(value) {
    return typeof value === "string" && !/[\u0000-\u001f\u007f-\u009f]/u.test(value) && !Array.from(value).some(function (c) { return c.length === 1 && /[\ud800-\udfff]/.test(c); });
  }
  function validURL(value) {
    if (!clean(value) || value.trim() !== value || new TextEncoder().encode(value).length > 2048) return false;
    var parts = /^https:\/\/([a-z0-9.-]+)(?::([1-9][0-9]{0,4}))?(\/[^?#]*)?$/.exec(value);
    if (!parts || parts[1].length > 253 || !parts[1].endsWith(".ts.net") || parts[1].split(".").length < 4 || !parts[1].split(".").every(function (s) { return s.length <= 63 && /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/.test(s); })) return false;
    if (parts[2] && (Number(parts[2]) > 65535 || parts[2] === "443")) return false;
    try {
      var path = parts[3] || "", decoded = decodeURIComponent(path);
      if (!clean(decoded) || /[\s\p{Cf}\\%?#]/u.test(decoded) || decoded.includes("//") || decoded.split("/").some(function (s) { return s === "." || s === ".."; })) return false;
      // Match Go's canonical escaped path; reject rather than repair spelling.
      var canonical = encodeURI(decoded).replace(/[!'()*]/g, function (c) { return "%" + c.charCodeAt(0).toString(16).toUpperCase(); });
      return path === canonical;
    } catch (_) { return false; }
  }
  function validFields(value) {
    return clean(value.name) && value.name.length > 0 && value.name.trim() === value.name && Array.from(value.name).length <= 120 && validURL(value.url) && (value.icon === undefined || typeof value.icon === "string" && icons.has(value.icon));
  }
  function documentBytes(value) {
    // Go json.Marshal additionally escapes HTML and the two JS line separators.
    return new TextEncoder().encode(JSON.stringify(value).replace(new RegExp("[<>&" + String.fromCharCode(8232, 8233) + "]", "g"), function (c) { return "\\u" + c.charCodeAt(0).toString(16).padStart(4, "0"); })).length;
  }
  function validDocument(value) {
    if (!keys(value, ["version", "entries"]) || value.version !== 1 || !Array.isArray(value.entries) || value.entries.length > 128) return false;
    var ids = new Set();
    return value.entries.every(function (entry) {
      if (!keys(entry, ["id", "name", "url"], ["icon"]) || !personalID(entry.id) || ids.has(entry.id) || !validFields(entry)) return false;
      ids.add(entry.id); return true;
    }) && documentBytes(value) <= 65536;
  }
  function validBackup(value) {
    return keys(value, ["version", "generation", "revision", "document"]) && value.version === 1 && generation(value.generation) && revision(value.revision) && value.revision !== "0" && validDocument(value.document);
  }
  function validState(value) {
    if (!keys(value, ["generation", "revision", "initialized", "document", "csrf", "identity_scope", "icons", "projection"]) || !generation(value.generation) || !revision(value.revision) || typeof value.initialized !== "boolean" || !validDocument(value.document) || value.identity_scope !== scope || typeof value.csrf !== "string" || !/^[0-9]{1,20}\.[a-f0-9]{64}$/.test(value.csrf)) return false;
    if (value.initialized !== (value.revision !== "0") || !value.initialized && value.document.entries.length) return false;
    if (!Array.isArray(value.icons) || value.icons.length !== icons.size - 1 || new Set(value.icons).size !== value.icons.length || !value.icons.every(function (id) { return id !== "" && icons.has(id); })) return false;
    if (!Array.isArray(value.projection) || value.projection.length !== value.document.entries.length) return false;
    var ids = new Set(value.document.entries.map(function (e) { return e.id; }));
    return value.projection.every(function (p) { if (!keys(p, ["id", "status"]) || !ids.delete(p.id) || !["active", "unavailable"].includes(p.status)) return false; return true; });
  }
  function parseBackup(text) {
    if (typeof text !== "string" || text.length > 100000) throw new Error("invalid_copy");
    var value = JSON.parse(text);
    if (!validBackup(value)) throw new Error("invalid_copy");
    return value;
  }
  function authoredCopy(value) { return { version: 1, generation: value.generation, revision: value.revision, document: value.document }; }
  function observeBackup(value) {
    // Disk selects the recovery generation. Within that generation, never forget
    // a higher validated copy merely because a delayed writer regressed disk.
    if (value && (!backup || value.generation !== backup.generation || compare(value.revision, backup.revision) > 0)) backup = value;
  }
  function readBackup() {
    try {
      var text = window.localStorage.getItem(key);
      return text === null ? null : parseBackup(text);
    } catch (error) {
      storageProblem = error.message === "invalid_copy" || error instanceof SyntaxError ? "This browser’s recovery copy is invalid and was not used." : "Browser recovery storage is unavailable.";
      return null;
    }
  }
  function needsChoice() { return !!(state && backup && backup.generation !== state.generation); }
  function remember(explicit) {
    // This check is not a cross-tab transaction. The storage listener below repairs
    // interleaved same-generation regressions using retained/event evidence.
    var disk = readBackup();
    observeBackup(disk);
    if (!explicit && needsChoice()) return false;
    if (backup && backup.generation === state.generation && compare(backup.revision, state.revision) > 0) return false;
    if (!state.initialized && !explicit) return false; // absence NEVER erases a copy
    try {
      if (!state.initialized) { window.localStorage.removeItem(key); backup = null; }
      else {
        var copy = authoredCopy(state);
        window.localStorage.setItem(key, JSON.stringify(copy)); backup = copy;
      }
      storageProblem = ""; return true;
    } catch (_) { storageProblem = "Saved on server; this browser’s recovery copy is unavailable."; return false; }
  }
  function compatible(node) {
    return !invalid && node && node.getAttribute("data-personal-enabled") === "true" && node.getAttribute("data-identity-scope") === scope && node.getAttribute("data-service-edit-mode") === mode;
  }
  function controls() {
    var ready = !!(state && !invalid && !posting && !loading && !needsChoice() && !recoveryChoice);
    fields.disabled = !ready || !draft || draft.blocked;
    save.disabled = fields.disabled; remove.disabled = fields.disabled;
    byID("personal-editor-cancel").disabled = posting;
    document.querySelectorAll("[data-add-personal], [data-edit-personal]").forEach(function (b) { b.hidden = invalid; b.disabled = !ready; });
    restore.disabled = posting || loading || recoveryChoice; adopt.disabled = posting || loading; refresh.disabled = posting || loading; reload.disabled = posting || loading;
  }
  function message(text, error) { status.textContent = text; status.setAttribute("data-error", String(!!error)); }
  function values() { return { name: name.value, url: url.value, icon: icon.value }; }
  function dirty() { return draft && draft.baseline !== JSON.stringify(values()); }
  function preview() { byID("personal-editor-preview").src = icons.get(icon.value) || icons.get(""); }
  function close(force) {
    if (!force && posting) return;
    if (!force && dirty() && !window.confirm("Discard your unsaved service changes?")) return;
    var opener = draft && draft.opener; draft = null; form.reset(); reload.hidden = true; dialog.close(); message(""); controls();
    if (!invalid) {
      if (opener && opener.isConnected && !opener.closest("[hidden]")) opener.focus();
      else { var heading = byID("services-heading"); if (heading) { heading.setAttribute("tabindex", "-1"); heading.focus(); } }
    }
  }
  function invalidate() {
    invalid = true; epoch++; if (controller) controller.abort(); state = null; backup = null; posting = false; loading = false;
    close(true); list.replaceChildren(); manager.hidden = true; summary.textContent = ""; byID("personal-recovery-help").textContent = "";
    if (root()) root().querySelectorAll("[data-personal-service]").forEach(function (card) { card.remove(); });
    notice.textContent = "Personal services are no longer available. Refresh the portal before continuing.";
    var search = byID("portal-search-input"); if (search) search.value = "";
    if (window.__velociportalApplyPortalSearch) window.__velociportalApplyPortalSearch();
    controls();
  }
  function renderManager() {
    if (invalid) return;
    manager.hidden = false;
    var focusedID = list.contains(document.activeElement) && document.activeElement.getAttribute("data-edit-personal");
    list.replaceChildren();
    if (state) {
      state.document.entries.forEach(function (entry) {
        var li = document.createElement("li"), text = document.createElement("span"), label = document.createElement("span"), availability = document.createElement("small"), button = document.createElement("button");
        text.className = "personal-entry"; label.textContent = entry.name;
        // Never turn an authored record into a bypass link, even when marked active.
        var active = !!root().querySelector('[data-personal-service="' + entry.id + '"]');
        availability.textContent = active ? "Shown in Services" : "Not currently available";
        text.appendChild(label); text.appendChild(availability); button.type = "button"; button.className = "personal-action"; button.textContent = "Edit";
        button.setAttribute("data-edit-personal", entry.id); button.setAttribute("aria-label", "Edit service: " + entry.name); button.setAttribute("aria-haspopup", "dialog"); button.setAttribute("aria-controls", "personal-editor");
        li.appendChild(text); li.appendChild(button); list.appendChild(li);
      });
    }
    var choice = needsChoice();
    recovery.hidden = !choice; restore.hidden = !choice || state.initialized; adopt.hidden = !choice;
    byID("personal-recovery-help").textContent = choice ? "The server restarted. This browser has a copy of " + backup.document.entries.length + " services from before the restart. We cannot tell whether it is the newest. " + (state.initialized ? "A current server collection already exists; restoring over it is not allowed." : "Restore it only if you want to use this copy.") : "";
    adopt.textContent = state && state.initialized ? "Use current server copy" : "Start without this copy";
    summary.textContent = storageProblem || (choice ? "Choose which copy to keep before editing." : state ? (state.document.entries.length ? "Personal to your account. Edit includes delete." : "No personal services yet. Use Add service to save a tailnet launch link.") : "Loading your services…");
    controls();
    if (focusedID && !dialog.open) {
      var replacement = list.querySelector('[data-edit-personal="' + focusedID + '"]');
      if (replacement && !replacement.disabled) replacement.focus();
      else { summary.setAttribute("tabindex", "-1"); summary.focus(); }
    }
  }
  function blockDraft(text) {
    if (!draft) return;
    draft.blocked = true; reload.hidden = false; message(text, true); controls();
  }
  function refreshCards() { if (window.htmx && compatible(root())) window.htmx.trigger(document.body, "personal-services-saved"); }
  function accept(value, explicit) {
    if (retired.has(value.generation)) throw new Error("stale_response");
    if (state && state.generation === value.generation && compare(value.revision, state.revision) < 0) throw new Error("stale_response");
    var disk = readBackup();
    observeBackup(disk);
    if (backup && backup.generation === value.generation && compare(backup.revision, value.revision) > 0) throw new Error("stale_response");
    var changed = state && (state.generation !== value.generation || state.revision !== value.revision);
    if (state && state.generation !== value.generation) retired.add(state.generation);
    state = value; tokenReceivedAt = Date.now(); tokenReceivedTick = monotonicNow();
    if (changed && draft) blockDraft("Your services changed elsewhere. Your draft is kept. Reload for review before saving.");
    var persisted = remember(explicit);
    renderManager();
    return persisted;
  }
  async function request(method, body) {
    epoch++; var ticket = epoch;
    if (controller) controller.abort(); controller = new AbortController();
    var activeController = controller, timer = window.setTimeout(function () { activeController.abort(); }, 15000);
    try {
      var options = { method: method, credentials: "same-origin", cache: "no-store", redirect: "error", signal: activeController.signal, headers: { Accept: "application/json" } };
      if (body) { options.headers["Content-Type"] = "application/json"; options.body = JSON.stringify(body); }
      var response = await window.fetch("/api/personal-services", options);
      if (ticket !== epoch || !compatible(root())) return null;
      if ([401, 403, 404].includes(response.status)) { invalidate(); return null; }
      if (!/^application\/json(?:\s*;|$)/i.test(response.headers.get("Content-Type") || "")) throw new Error("invalid_response");
      var raw = await response.text();
      if (ticket !== epoch || !compatible(root())) return null;
      if (raw.length > 131072) throw new Error("invalid_response");
      var value = JSON.parse(raw);
      if (response.status !== 200) {
        var codes = {400:["invalid_request"],409:["conflict"],413:["body_too_large"],415:["unsupported_media_type"],503:["busy","unavailable"],507:["capacity"]};
        if (!keys(value, ["error"]) || !codes[response.status] || !codes[response.status].includes(value.error)) throw new Error("invalid_response");
        var error = new Error(value.error); error.code = value.error; throw error;
      }
      if (!validState(value)) {
        if (value && value.identity_scope && value.identity_scope !== scope) { invalidate(); return null; }
        throw new Error("invalid_response");
      }
      return value;
    } catch (error) {
      if (ticket !== epoch || invalid) return null;
      throw error;
    } finally { window.clearTimeout(timer); }
  }
  async function load(review, hint) {
    if (invalid || posting) return;
    hint = hint || root();
    var nextHint = hint.getAttribute("data-personal-generation") + ":" + hint.getAttribute("data-personal-revision");
    if (loading && loadingHint === nextHint) return;
    loadingHint = nextHint; loading = true; controls(); refresh.hidden = true;
    var pending = request("GET"), ticket = epoch;
    try {
      var value = await pending; if (!value) return;
      var currentRoot = root();
      if (currentRoot.getAttribute("data-personal-generation") === value.generation && compare(value.revision, currentRoot.getAttribute("data-personal-revision")) < 0) throw new Error("stale_response");
      accept(value, false); loading = false;
      if (review && draft) {
        var entry = value.document.entries.find(function (e) { return e.id === draft.id; });
        if (draft.id && !entry) { close(true); notice.textContent = "This service was deleted elsewhere. Your services are up to date."; }
        else populate(entry);
      }
      var marker = root();
      if (marker.getAttribute("data-personal-generation") !== value.generation || marker.getAttribute("data-personal-revision") !== value.revision) refreshCards();
    } catch (_) {
      summary.textContent = "Current services could not be loaded. Reload to try again; no changes were sent."; refresh.hidden = false;
      if (draft) blockDraft("Current services could not be loaded. Your draft is kept; reload for review.");
    } finally { if (ticket === epoch) { loading = false; controls(); } }
  }
  function populate(entry) {
    name.value = entry ? entry.name : ""; url.value = entry ? entry.url : ""; icon.value = entry ? entry.icon || "" : "";
    draft.generation = state.generation; draft.revision = state.revision; draft.blocked = false; draft.baseline = JSON.stringify(values());
    remove.hidden = !entry; reload.hidden = true; preview(); message(""); controls(); name.focus();
  }
  function open(id, opener) {
    if (dialog.open || !state || posting || loading || needsChoice() || recoveryChoice || !compatible(root())) return;
    var entry = id && state.document.entries.find(function (e) { return e.id === id; });
    if (id && !entry) return;
    draft = { id: id || "", opener: opener }; byID("personal-editor-title").textContent = entry ? "Edit service" : "Add service";
    notice.textContent = "";
    // Close the incumbent account sheet before opening the native modal, so its
    // capture-phase Escape handler cannot compete with the editor's focus flow.
    var account = byID("account-panel"), accountTrigger = byID("account-trigger");
    if (account && !account.hidden && accountTrigger) accountTrigger.click();
    dialog.showModal(); populate(entry);
  }
  async function mutate(action) {
    if (!state || posting || loading || invalid || (action !== "restore" && (!draft || draft.blocked || needsChoice() || recoveryChoice))) return;
    if (draft && (draft.generation !== state.generation || draft.revision !== state.revision)) { blockDraft("Your services changed. Reload for review before saving."); return; }
    var previous = state.document, candidate = backup && JSON.stringify(backup), originalDraft = draft, sent = false;
    var body = { action: action, generation: state.generation, revision: state.revision, csrf: state.csrf };
    if (action === "restore") {
      if (!needsChoice() || state.initialized) return;
      body.document = backup.document;
    } else {
      if (draft.id) body.id = draft.id;
      if (action !== "delete") {
        var value = values();
        if (!validFields(value)) { message("Check the fields: use an unpadded name (up to 120 characters), a canonical HTTPS device.tailnet.ts.net launch link without a query or fragment, and a listed icon.", true); return; }
        Object.assign(body, value);
      }
    }
    posting = true; controls(); message(action === "delete" ? "Deleting service…" : "Saving service…");
    if (window.htmx) window.htmx.trigger(root(), "htmx:abort");
    try {
      if (tokenNeedsRefresh()) {
        message("Checking current services before saving…");
        var fresh = await request("GET"); if (!fresh) return;
        accept(fresh, false);
        // A token refresh must not silently rebase an old draft or recovery choice.
        if (fresh.generation !== body.generation || fresh.revision !== body.revision ||
            action !== "restore" && (draft !== originalDraft || !draft || draft.blocked || needsChoice()) ||
            action === "restore" && (fresh.initialized || JSON.stringify(backup) !== candidate)) {
          blockDraft("Your services changed elsewhere. Your draft is kept. Reload for review before saving.");
          summary.textContent = "The server or browser copy changed. Reload current services before continuing.";
          refresh.hidden = false;
          if (action === "restore") recoveryChoice = true;
          refreshCards(); return;
        }
        body.csrf = fresh.csrf;
      }
      sent = true;
      var result = await request("POST", body); if (!result) return;
      // A success must advance this exact generation. Other outcomes are uncertain.
      if (!confirms(result, body, previous)) throw new Error("invalid_confirmation");
      // A tab may have replaced the recovery candidate while this POST ran.
      // Explicit restore consents to replacing the selected copy, not any later copy.
      var disk = readBackup();
      var persisted = accept(result, action === "restore" && (!disk || JSON.stringify(disk) === candidate)); posting = false; close(true);
      notice.textContent = persisted ? (action === "delete" ? "Service deleted." : action === "restore" ? "Browser copy restored. Its freshness cannot be verified." : "Service saved.") : "Saved on server; this browser’s recovery copy is unavailable.";
      refreshCards();
    } catch (error) {
      posting = false;
      if (!sent) {
        // No mutation has been sent: do not call this an uncertain save or retry it.
        blockDraft("Not saved. Current services could not be checked. Your draft is kept; reload for review.");
        summary.textContent = "No changes were sent. Reload current services before continuing.";
        refresh.hidden = false;
        if (action === "restore") recoveryChoice = true;
        return;
      }
      if (error.code) {
        if (error.code === "invalid_request" || error.code === "body_too_large") message("Not saved. Check the fields and collection size. Your draft is kept.", true);
        else if (error.code === "conflict") {
          blockDraft("Your services changed elsewhere. Your draft is kept. Reload for review before saving.");
          summary.textContent = "The server copy changed. Reload current services before continuing."; refresh.hidden = false;
          if (action === "restore") recoveryChoice = true;
        } else {
          message(error.code === "capacity" ? "Not saved. The personal-service limit has been reached. Your draft is kept." : "Not saved. The server is unavailable or busy. Your draft is kept; reload for review.", true);
          if (draft) { draft.blocked = true; reload.hidden = false; }
          refresh.hidden = false;
        }
      } else {
        // The server may have committed. Exactly one GET, never a repeated POST.
        blockDraft("No save confirmation arrived. Checking the current server copy; your draft is kept.");
        summary.textContent = "No save confirmation arrived. Checking the server copy…";
        posting = true; loading = true; controls();
        try {
          var current = await request("GET");
          if (current) {
            accept(current, false);
            message("The save outcome is uncertain. The current server copy is available; reload for review before making another change.", true);
            summary.textContent = "Save confirmation was lost. Review the current server copy before continuing.";
            refreshCards();
          }
        } catch (_) {
          message("The save outcome is uncertain and the server could not be checked. Do not repeat the save. Reload for review when the portal is available.", true);
          summary.textContent = "Save outcome unknown. Reload current services when the portal is available.";
        } finally { loading = false; refresh.hidden = false; }
        if (action === "restore") recoveryChoice = true;
      }
    } finally { posting = false; controls(); }
  }
  document.addEventListener("click", function (event) {
    var add = event.target.closest("[data-add-personal]"), edit = event.target.closest("[data-edit-personal]");
    if (add) open("", add); else if (edit) open(edit.getAttribute("data-edit-personal"), edit);
  });
  form.addEventListener("submit", function (event) { event.preventDefault(); mutate(draft && draft.id ? "update" : "create"); });
  remove.addEventListener("click", function () { if (!remove.disabled && window.confirm("Delete this personal service from the current server collection?")) mutate("delete"); });
  byID("personal-editor-cancel").addEventListener("click", function () { close(false); });
  dialog.addEventListener("cancel", function (event) { event.preventDefault(); close(false); });
  dialog.addEventListener("close", function () { if (!dialog.open && draft) close(true); });
  icon.addEventListener("change", preview);
  reload.addEventListener("click", function () {
    if (dirty() && !window.confirm("Replace your unsaved draft with the current server values for review?")) return;
    recoveryChoice = false; load(true);
  });
  refresh.addEventListener("click", function () { recoveryChoice = false; load(false); });
  restore.addEventListener("click", function () {
    if (posting || loading || recoveryChoice || !needsChoice() || state.initialized) return;
    if (window.confirm("Restore this browser’s copy? It may be older than changes saved on another device before the restart.")) mutate("restore");
  });
  adopt.addEventListener("click", function () {
    if (posting || loading || !needsChoice()) return;
    if (!window.confirm("Discard this browser’s previous recovery copy and use the current server collection? This cannot be undone.")) return;
    if (remember(true)) { recoveryChoice = false; renderManager(); notice.textContent = "Using the current server collection."; }
    else { renderManager(); summary.textContent = "The previous browser copy could not be replaced. Recovery storage is unavailable; no server change was made."; }
  });
  window.addEventListener("storage", function (event) {
    if (event.key !== key && event.key !== null || invalid) return;
    var disk = readBackup();
    // Tab events do not seed the server, replace drafts, or authorize any card.
    // No repair after deletion/corruption or across generations: those need the
    // existing explicit recovery choice, not a guess about which run is newer.
    observeBackup(disk);
    if (disk && state && disk.generation === state.generation) {
      if (state.initialized) observeBackup(authoredCopy(state));
      if (event.key === key && typeof event.newValue === "string") {
        try {
          var observed = parseBackup(event.newValue);
          if (observed.generation === disk.generation) observeBackup(observed);
        } catch (_) {} // Untrusted events never supply a repair candidate.
      }
      if (backup && backup.generation === disk.generation && compare(backup.revision, disk.revision) > 0) {
        // At most one write per event, only for a strict revision increase. Equal
        // or delayed lower events do not echo writes. This converges after finite
        // interleavings when storage/events work; it is not cross-tab atomicity.
        try { window.localStorage.setItem(key, JSON.stringify(backup)); storageProblem = ""; }
        catch (_) { storageProblem = "Browser recovery storage is unavailable; its saved copy may be older."; }
      }
    }
    renderManager();
    if (draft) blockDraft("The browser recovery copy changed in another tab. Your draft is kept; reload for review.");
  });
  document.body.addEventListener("htmx:beforeSwap", function (event) {
    var detail = event.detail;
    if (!detail || !detail.target || detail.target.id !== "portal-content") return;
    var incoming = new DOMParser().parseFromString(detail.xhr.responseText, "text/html").getElementById("portal-content");
    if (!compatible(incoming)) { invalidate(); return; }
    var gen = incoming.getAttribute("data-personal-generation"), rev = incoming.getAttribute("data-personal-revision"), initialized = incoming.getAttribute("data-personal-initialized");
    if (!generation(gen) || !revision(rev) || initialized !== String(rev !== "0")) { detail.shouldSwap = false; return; }
    if (posting || retired.has(gen) || state && gen === state.generation && compare(rev, state.revision) < 0) { detail.shouldSwap = false; return; }
    if (state && gen !== state.generation) {
      detail.shouldSwap = false;
      // An unknown generation is not "newer" merely because its fragment arrived last.
      load(false, incoming);
      return;
    }
    if (loading && !state && gen !== root().getAttribute("data-personal-generation")) { load(false, incoming); detail.shouldSwap = false; }
  });
  document.body.addEventListener("htmx:afterSwap", function () {
    if (!compatible(root())) { invalidate(); return; }
    renderManager();
    if (!posting && (!state || root().getAttribute("data-personal-generation") !== state.generation || root().getAttribute("data-personal-revision") !== state.revision)) load(false);
  });
  document.body.addEventListener("htmx:responseError", function (event) {
    if (event.detail && event.detail.target && event.detail.target.id === "portal-content" && [401,403].includes(event.detail.xhr.status)) invalidate();
  });
  backup = readBackup(); renderManager(); load(false);
}());
