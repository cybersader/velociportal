/* Shared presentation only. Drafts, search and opaque form tokens stay in memory. */
(function () {
  "use strict";
  var dialog = document.getElementById("service-editor");
  if (!dialog || typeof dialog.showModal !== "function" || !window.AbortController) {
    function hideUnsupportedControls() {
      document.querySelectorAll("[data-edit-service]").forEach(function (b) { b.hidden = true; });
    }
    hideUnsupportedControls();
    document.body.addEventListener("htmx:afterSwap", hideUnsupportedControls);
    return;
  }
  var form = document.getElementById("service-editor-form");
  var fields = document.getElementById("service-editor-fields");
  var name = document.getElementById("service-editor-name");
  var url = document.getElementById("service-editor-url");
  var icon = document.getElementById("service-editor-icon");
  var preview = document.getElementById("service-editor-preview");
  var status = document.getElementById("service-editor-status");
  var save = document.getElementById("service-editor-save");
  var reset = document.getElementById("service-editor-reset");
  var reload = document.getElementById("service-editor-reload");
  var announcement = document.getElementById("service-editor-announcement");
  var initialScope = content().getAttribute("data-identity-scope");
  var session = null, epoch = 0, controller = null;

  function content() { return document.getElementById("portal-content"); }
  function eligible(id) {
    var root = content();
    // Search-hidden remains authorized: never use :visible or the hidden property.
    return !!root && root.getAttribute("data-identity-scope") === initialScope &&
      root.getAttribute("data-service-editing") === "true" &&
      !!root.querySelector('[data-edit-service="' + id + '"]');
  }
  function invalidate() { epoch++; if (controller) controller.abort(); controller = null; }
  function message(text, error) { status.textContent = text; status.setAttribute("data-error", String(!!error)); }
  function controls(ready) { fields.disabled = !ready; save.disabled = !ready; reset.disabled = !ready; }
  function values() { return { name: name.value, url: url.value, icon: icon.value }; }
  function dirty() { return session && session.loaded && JSON.stringify(values()) !== session.baseline; }
  function close(force) {
    if (!session && !dialog.open) return;
    if (!force && dirty() && !window.confirm("Discard your unsaved service changes?")) return;
    var opener = session && session.opener, id = session && session.id;
    invalidate(); session = null; form.reset(); controls(false); reload.hidden = true;
    dialog.close();
    if (opener && opener.isConnected && !opener.closest("[hidden]")) opener.focus();
    else {
      var root = content(), replacement = root && root.querySelector('[data-edit-service="' + id + '"]');
      if (replacement && !replacement.closest("[hidden]")) replacement.focus();
      else { var heading = document.getElementById("services-heading"); if (heading) { heading.setAttribute("tabindex", "-1"); heading.focus(); } }
    }
  }
  function previewIcon() {
    var option = icon.options[icon.selectedIndex];
    preview.src = option ? option.getAttribute("data-icon-path") : "/static/service-icons/generic.svg";
  }
  function current(ticket) { return session && dialog.open && ticket === epoch && eligible(session.id); }
  async function request(method, body) {
    invalidate(); var ticket = epoch, id = session.id;
    controller = new AbortController();
    var options = { method: method, credentials: "same-origin", cache: "no-store", signal: controller.signal, headers: {} };
    if (body) { options.headers["Content-Type"] = "application/json"; options.body = JSON.stringify(body); }
    try {
      var response = await fetch("/api/service-metadata/" + id, options);
      if (!current(ticket)) return null;
      // The outer identity middleware can return plain text, not API JSON.
      if (response.status === 401 || response.status === 403 || response.status === 404) {
        close(true); announcement.textContent = "Editing is no longer available. Refresh the portal before continuing."; return null;
      }
      var data = await response.json();
      if (!current(ticket)) return null;
      if (!response.ok) { var error = new Error(data.error || "request_failed"); error.code = data.error; throw error; }
      return data;
    } catch (error) {
      if (!current(ticket) || error.name === "AbortError") return null;
      throw error;
    }
  }
  function explain(error, posting) {
    controls(false); reload.hidden = false;
    var text;
    if (error.code === "conflict") text = "The shared file or service changed. Your draft is kept. Reload current values for review before saving again. If an operator changed the file, stop editing and restart the server.";
    else if (error.code === "uncertain") { text = "The save outcome is uncertain. Do not retry. An operator must reconcile the metadata file and restart editing."; reload.hidden = true; }
    else if (error.code === "unavailable") text = "A fresh complete snapshot is required. Your draft is kept; reload after the portal recovers.";
    else if (error.code === "invalid_fields" || error.code === "invalid_request") { text = "Check the fields: use an unpadded name, a complete HTTP(S) URL, and a listed local icon. Your draft is kept."; controls(true); reload.hidden = true; }
    else if (error.code === "storage_unavailable") text = "Metadata storage is unavailable. Your draft is kept. Ask the operator to check the dedicated directory before trying again.";
    else { text = posting ? "No save confirmation was received. The file may have changed. Your draft is kept; reload and review before trying again." : "Current values could not be loaded. Reload to try again."; }
    message(text, true);
  }
  async function load() {
    controls(false); reload.hidden = true; message("Loading current values…");
    try {
      var data = await request("GET"); if (!data) return;
      if (data.identity_scope !== initialScope || data.id !== Number(session.id)) { close(true); return; }
      session.tokens = { revision: data.revision, target_revision: data.target_revision, csrf: data.csrf };
      name.value = data.fields.name; url.value = data.fields.url; icon.value = data.fields.icon;
      document.getElementById("service-editor-defaults").textContent = "Empty fields use defaults: " + data.defaults.name + (data.defaults.url ? " — " + data.defaults.url : " — no concrete link") + ". Icon defaults to generic.";
      name.placeholder = data.defaults.name; url.placeholder = data.defaults.url || "https://service.example.com";
      session.baseline = JSON.stringify(values()); session.loaded = true; previewIcon(); controls(true); message("");
      if (document.activeElement === document.getElementById("service-editor-title")) name.focus();
    } catch (error) { explain(error, false); }
  }
  document.addEventListener("click", function (event) {
    var button = event.target.closest("[data-edit-service]");
    if (!button || dialog.open || !eligible(button.getAttribute("data-edit-service"))) return;
    session = { id: button.getAttribute("data-edit-service"), opener: button, loaded: false };
    announcement.textContent = ""; form.reset(); previewIcon(); controls(false); dialog.showModal(); load();
  });
  document.getElementById("service-editor-cancel").addEventListener("click", function () { close(false); });
  dialog.addEventListener("cancel", function (event) { event.preventDefault(); close(false); });
  // A programmatic close also invalidates every pending completion.
  dialog.addEventListener("close", function () {
    // Native close events are queued: an old event must not clear a newly opened form.
    if (dialog.open) return;
    invalidate(); session = null; form.reset(); controls(false);
  });
  icon.addEventListener("change", previewIcon);
  reload.addEventListener("click", function () {
    if (dirty() && !window.confirm("Replace your unsaved draft with the current shared values for review?")) return;
    load();
  });
  async function submit(action) {
    if (!session || !session.loaded || save.disabled || !eligible(session.id)) return;
    var body = Object.assign({ action: action }, session.tokens);
    if (action === "save") Object.assign(body, values());
    session.posting = true; controls(false); message(action === "reset" ? "Resetting shared fields…" : "Saving shared fields…");
    // Cancel old fragment polls; their captured pre-save presentation must not win.
    if (window.htmx && content()) window.htmx.trigger(content(), "htmx:abort");
    try {
      var data = await request("POST", body); if (!data) return;
      if (data.status !== "saved") throw new Error("missing_confirmation");
      close(true); announcement.textContent = action === "reset" ? "Name, link and icon reset for everyone. Category and order are unchanged." : "Service changes saved for everyone.";
      if (window.htmx) window.htmx.trigger(document.body, "service-metadata-saved");
    } catch (error) { if (session) { session.posting = false; explain(error, true); } }
  }
  form.addEventListener("submit", function (event) { event.preventDefault(); submit("save"); });
  reset.addEventListener("click", function () {
    if (window.confirm("Reset this service’s name, link and icon for everyone? Category and order remain unchanged.")) submit("reset");
  });
  function reconcile() {
    if (session && !eligible(session.id)) { close(true); announcement.textContent = "Editing closed because the identity or available service changed."; }
    var root = content();
    if (root && root.getAttribute("data-identity-scope") !== initialScope) {
      root.querySelectorAll("[data-edit-service]").forEach(function (button) { button.hidden = true; });
      var search = document.getElementById("portal-search-input"); if (search) search.value = "";
      if (window.__velociportalApplyPortalSearch) window.__velociportalApplyPortalSearch();
    }
  }
  document.body.addEventListener("htmx:afterSwap", reconcile);
  document.body.addEventListener("htmx:beforeSwap", function (event) {
    if (!event.detail || event.detail.target.id !== "portal-content") return;
    var parsed = new DOMParser().parseFromString(event.detail.xhr.responseText, "text/html").getElementById("portal-content");
    if (session && parsed && (parsed.getAttribute("data-identity-scope") !== initialScope || parsed.getAttribute("data-service-editing") !== "true" || !parsed.querySelector('[data-edit-service="' + session.id + '"]'))) close(true);
    else if (session && session.posting) event.detail.shouldSwap = false;
  });
  document.body.addEventListener("htmx:responseError", function (event) {
    if (event.detail && event.detail.target.id === "portal-content" && [401,403].indexOf(event.detail.xhr.status) !== -1) { close(true); announcement.textContent = "Editing is no longer available. Refresh the portal."; }
  });
}());
