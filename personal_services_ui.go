package main

import (
	"fmt"
	"html"
	"net/http"
	"strings"
)

// Navigation permits Sec-Fetch-Mode:navigate, unlike the JSON API. Preserve its
// exact raw identity/Host guards without treating navigation as an API request.
func personalRenderIdentity(store *PersonalServicesStore, r *http.Request, identity *Identity) bool {
	if store == nil || identity == nil || r.Host != store.config.Host || !validPersonalLogin(identity.Login) {
		return false
	}
	for _, key := range []string{"Tailscale-User-Login", "Tailscale-User-Name", "Tailscale-User-Profile-Pic"} {
		value, single, present := singleRawHeader(r.Header, key)
		if present && !single || key == "Tailscale-User-Login" && (!single || value != identity.Login) {
			return false
		}
	}
	return true
}

// Only synchronization markers enter the poll region. Authored recovery data and
// CSRF tokens come from the identity-scoped API, never from a second catalog.
func renderPersonalServicesUI(state *PersonalServicesState) (markers, add, manager, editor string) {
	if state == nil {
		return `data-personal-enabled="false"`, "", "", ""
	}
	markers = fmt.Sprintf(`data-personal-enabled="true" data-personal-generation="%s" data-personal-revision="%s" data-personal-initialized="%t"`, html.EscapeString(state.Generation), html.EscapeString(state.Revision), state.Initialized)
	add = `<button type="button" class="personal-action" data-add-personal aria-haspopup="dialog" aria-controls="personal-editor" hidden>Add service</button>`
	manager = `<section class="account-panel-section" id="personal-manager" aria-labelledby="personal-manager-title" hidden>
<h3 class="account-panel-heading" id="personal-manager-title">Your services</h3>
<p class="personal-hint" id="personal-manager-status" role="status" aria-live="polite">Loading your services…</p>
<ul id="personal-list" class="personal-list"></ul>
<div id="personal-recovery" hidden><p class="personal-hint" id="personal-recovery-help"></p>
<button type="button" class="personal-action" id="personal-restore" hidden>Restore from this browser</button>
<button type="button" class="personal-action" id="personal-adopt" hidden>Use current server copy</button></div>
<button type="button" class="personal-action" id="personal-refresh" hidden>Reload current services</button>
</section>`
	var choices strings.Builder
	choices.WriteString(`<option value="" data-icon-path="/static/service-icons/generic.svg">Default (generic service)</option>`)
	for _, icon := range serviceIcons {
		fmt.Fprintf(&choices, `<option value="%s" data-icon-path="%s">%s</option>`, icon.ID, icon.Path, html.EscapeString(icon.Label))
	}
	editor = `<p class="service-editor-announcement" id="personal-announcement" role="status" aria-live="polite"></p>
<dialog id="personal-editor" class="service-editor" aria-labelledby="personal-editor-title" aria-describedby="personal-editor-help">
<form id="personal-editor-form">
<h2 id="personal-editor-title" tabindex="-1" autofocus>Add service</h2>
<p id="personal-editor-help">Only for you. Available across your devices while this server runs. After a restart, a browser copy can be restored, but it may not be the newest.</p>
<p id="personal-editor-status" role="status" aria-live="polite"></p>
<fieldset id="personal-editor-fields" disabled>
<label for="personal-editor-name">Display name</label>
<input id="personal-editor-name" type="text" maxlength="240" autocomplete="off" spellcheck="false" required>
<label for="personal-editor-url">Launch link</label>
<input id="personal-editor-url" type="text" inputmode="url" maxlength="2048" autocomplete="off" spellcheck="false" autocapitalize="none" placeholder="https://device.tailnet.ts.net" aria-describedby="personal-url-help" required>
<p id="personal-url-help" class="service-editor-hint">Use the normal HTTPS launch address, with an optional port and path. No query strings or pairing links. A saved link appears only when current policy evidence permits it; reachability is not checked.</p>
<label for="personal-editor-icon">Local icon</label>
<div class="service-icon-picker"><img id="personal-editor-preview" src="/static/service-icons/generic.svg" width="40" height="40" alt=""><select id="personal-editor-icon">` + choices.String() + `</select></div>
</fieldset>
<div class="service-editor-actions"><button type="submit" class="service-editor-primary" id="personal-editor-save" disabled>Save</button><button type="button" id="personal-editor-cancel">Cancel</button></div>
<div class="service-editor-secondary"><button type="button" id="personal-editor-delete" hidden>Delete service</button><button type="button" id="personal-editor-reload" hidden>Reload for review</button></div>
</form></dialog>
<script src="/static/personal-services.js" defer></script>`
	return
}

const personalServicesCSS = `
.services-heading-row { display: flex; align-items: baseline; justify-content: space-between; gap: 1rem; margin-bottom: .85rem; }
.services-heading-row .section-title { margin-bottom: 0; }
.personal-action { min-height: 44px; padding: .45rem .65rem; border: 1px solid var(--border); border-radius: 8px; background: var(--card-bg); color: var(--text); font: inherit; font-size: .85rem; cursor: pointer; }
.personal-action:hover:not(:disabled) { background: var(--card-hover-bg); border-color: var(--accent); }
.personal-action:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
.personal-action:disabled { opacity: .55; cursor: not-allowed; }
.personal-action[hidden], .service-edit-button[hidden] { display: none; }
.personal-hint { margin: .45rem 0; color: var(--muted); font-size: .85rem; line-height: 1.5; overflow-wrap: anywhere; }
.personal-list { list-style: none; margin: 0; padding: 0; max-height: 15rem; overflow-y: auto; overscroll-behavior: contain; }
.personal-list li { display: flex; align-items: center; justify-content: space-between; gap: .65rem; padding: .45rem 0; border-bottom: 1px solid var(--border); }
.personal-list .personal-entry { min-width: 0; overflow-wrap: anywhere; font-size: .9rem; }
.personal-list small { display: block; color: var(--muted); font-size: .8rem; }
.personal-list button { flex-shrink: 0; }
#personal-recovery .personal-action { margin: .25rem .25rem .25rem 0; }
#personal-editor-status:empty { display: none; }
#personal-editor-status[data-error="true"] { color: var(--health-unreachable); }
`
