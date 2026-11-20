package main

import (
	"fmt"
	"html"
	"strings"
)

func renderServiceEditor(enabled bool, modes ...serviceEditMode) string {
	if !enabled {
		return ""
	}
	mode := serviceEditShared
	if len(modes) > 0 {
		mode = modes[0]
	}
	help := "Changes are shared with everyone who can already see this service. They do not change access or backend checks."
	saveLabel, resetLabel := "Save for everyone", "Reset name/link/icon"
	nameMax, adapter := "120", ""
	if mode == serviceEditBrowser {
		help = "Saved in this browser."
		saveLabel, resetLabel = "Save", "Reset"
		// The validator counts Unicode code points, not UTF-16 input units.
		nameMax = "240"
		adapter = `<script src="/static/service-editor-browser.js" defer></script>`
	}
	var choices strings.Builder
	defaultIconLabel := "Default (generic service)"
	if mode == serviceEditBrowser {
		defaultIconLabel = "Default (server icon)"
	}
	fmt.Fprintf(&choices, `<option value="" data-icon-path="/static/service-icons/generic.svg">%s</option>`, defaultIconLabel)
	for _, icon := range serviceIcons {
		fmt.Fprintf(&choices, `<option value="%s" data-icon-path="%s">%s</option>`, icon.ID, icon.Path, html.EscapeString(icon.Label))
	}
	return `<p class="service-editor-announcement" id="service-editor-announcement" role="status" aria-live="polite"></p>
<dialog id="service-editor" data-edit-mode="` + string(mode) + `" class="service-editor" aria-labelledby="service-editor-title" aria-describedby="service-editor-help">
<form id="service-editor-form">
<h2 id="service-editor-title" tabindex="-1" autofocus>Edit service</h2>
<p id="service-editor-help">` + help + `</p>
<p id="service-editor-status" role="status" aria-live="polite">Loading current values…</p>
<fieldset id="service-editor-fields" disabled>
<label for="service-editor-name">Display name</label>
<input id="service-editor-name" type="text" maxlength="` + nameMax + `" autocomplete="off" spellcheck="false" aria-describedby="service-editor-defaults">
<label for="service-editor-url">Link URL</label>
<input id="service-editor-url" type="text" inputmode="url" maxlength="2048" autocomplete="off" spellcheck="false" autocapitalize="none" aria-describedby="service-editor-defaults">
<p id="service-editor-defaults" class="service-editor-hint">Leave fields empty to use the current defaults.</p>
<label for="service-editor-icon">Local icon</label>
<div class="service-icon-picker"><img id="service-editor-preview" src="/static/service-icons/generic.svg" width="40" height="40" alt=""><select id="service-editor-icon">` + choices.String() + `</select></div>
</fieldset>
<div class="service-editor-actions"><button id="service-editor-save" class="service-editor-primary" type="submit" disabled>` + saveLabel + `</button><button id="service-editor-cancel" type="button">Cancel</button></div>
<div class="service-editor-secondary"><button id="service-editor-reset" type="button" disabled>` + resetLabel + `</button><button id="service-editor-reload" type="button" hidden>Reload for review</button></div>
</form>
</dialog>
` + adapter + `<script src="/static/service-editor.js" defer></script>`
}

const editServicePencil = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="m14.5 5.5 4 4M4 20l4.9-1.1L20 7.8a2.8 2.8 0 0 0-4-4L4.9 15z"/></svg>`

const serviceEditorCSS = `
.service-icon { display: block; width: 32px; height: 32px; object-fit: contain; }
.service-domain { display: block; color: var(--muted); font-size: .8rem; overflow-wrap: anywhere; }
.card-editable, .card-monitored { display: flex; flex-direction: column; position: relative; padding: 0; gap: 0; overflow: hidden; }
/* A separate target, never an interactive child of the launch link. */
.card:has(> .health-status) { --dot-target: 44px; --dot-end: 4px; }
.card:has(> .health-status):has(> .service-edit-button:not([hidden])) { --dot-end: var(--edit-space, 3rem); }
.card:has(> .health-status) { --controls-space: calc(var(--dot-target) + var(--dot-end)); }
.card-monitored > .service-main { padding-inline-end: var(--controls-space); }
.health-status summary { position: absolute; inset-block-start: 6px; inset-inline-end: var(--dot-end); display: grid; place-items: center; width: var(--dot-target); height: var(--dot-target); list-style: none; cursor: pointer; border-radius: 6px; }
.health-status summary::before { content: ""; width: 7px; height: 7px; border-radius: 50%; border: 1px solid currentColor; background: currentColor; }
.health-unknown summary::before, .health-stale summary::before { background: transparent; }
.health-status summary:hover, .health-status summary:focus-visible { background: var(--card-hover-bg); }
.health-copy { display: block; position: relative; width: fit-content; margin: .25rem var(--dot-end) .55rem auto; max-width: calc(100% - var(--dot-end) - .75rem); padding: .35rem .65rem; border-radius: .85rem; background: var(--card-hover-bg); color: var(--muted); text-align: start; font-size: .8rem; font-weight: 500; }
.health-copy::before, .health-copy::after { content: ""; position: absolute; border-radius: 50%; background: var(--card-hover-bg); }
.health-copy::before { width: 6px; height: 6px; top: -5px; right: calc(var(--dot-target) / 2); }
.health-copy::after { width: 3px; height: 3px; top: -10px; right: calc(var(--dot-target) / 2 - 1px); }
@media (prefers-reduced-motion: no-preference) {
  .health-status::details-content { transform-origin: calc(100% - var(--dot-end) - var(--dot-target) / 2) top; opacity: 0; transform: scale(.92); transition: opacity 110ms ease-out, transform 150ms cubic-bezier(.16,1,.3,1), content-visibility 150ms allow-discrete; }
  .health-status[open]::details-content { opacity: 1; transform: none; }
}
@media (hover: hover) and (pointer: fine) and (not (any-pointer: coarse)) {
  .card:has(> .health-status) { --dot-target: 28px; }
}
.service-main { flex: 1; color: inherit; text-decoration: none; }
.card-editable > .service-main { padding-inline-end: var(--controls-space, .75rem); }
.card-editable:has(> .service-edit-button:not([hidden])) .service-main { padding-inline-end: var(--controls-space, var(--edit-space, 3rem)); }
a.service-main:hover, a.service-main:focus-visible { background: var(--card-hover-bg); }
/* The pencil sits across from the service icon, outside the launch link. */
.service-edit-button { position: absolute; inset-block-start: 4px; inset-inline-end: 4px; display: grid; place-items: center; width: 44px; height: 44px; padding: 0; border: 0; border-radius: 6px; background: transparent; color: var(--muted); cursor: pointer; }
.service-edit-button:hover, .service-edit-button:focus-visible { background: var(--card-hover-bg); color: var(--text); }
.service-edit-button:disabled { cursor: default; }
/* Keep touch and hybrid devices discoverable, even with a mouse attached. */
@media (hover: hover) and (pointer: fine) and (not (any-pointer: coarse)) {
  .card-editable { --edit-space: 2.5rem; }
  .service-edit-button { inset-block-start: 8px; inset-inline-end: 8px; width: 30px; height: 30px; opacity: 0; pointer-events: none; }
  .card-editable:hover .service-edit-button, .card-editable:focus-within .service-edit-button { opacity: 1; pointer-events: auto; }
}
.service-edit-button:focus-visible, .service-editor button:focus-visible, .service-editor input:focus-visible, .service-editor select:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }
.service-editor-announcement { margin: 0 1rem; color: var(--muted); font-size: .85rem; }
.service-editor-announcement:empty { display: none; }
.service-editor { width: min(32rem, calc(100vw - 2rem)); max-height: calc(100dvh - 2rem); padding: 1.5rem; border: 1px solid var(--border); border-radius: 14px; background: var(--card-bg); color: var(--text); overflow-y: auto; overscroll-behavior: contain; box-shadow: 0 16px 48px rgba(0,0,0,.3); }
.service-editor::backdrop { background: rgba(0,0,0,.5); }
.service-editor h2 { margin: 0 0 .6rem; font-size: 1.2rem; line-height: 1.3; }
.service-editor p { margin: 0 0 1rem; font-size: .85rem; color: var(--muted); overflow-wrap: anywhere; }
#service-editor-status:empty { display: none; }
#service-editor-status[data-error="true"] { color: var(--health-unreachable); }
.service-editor fieldset { display: grid; gap: .45rem; min-width: 0; margin: 0; padding: 0; border: 0; }
.service-editor label { font-size: .9rem; font-weight: 600; }
.service-editor input, .service-editor select { width: 100%; min-width: 0; min-height: 44px; padding: .6rem .7rem; margin-bottom: .65rem; border: 1px solid var(--border); border-radius: 8px; background: var(--bg); color: var(--text); font: inherit; caret-color: var(--accent); }
.service-icon-picker { display: flex; gap: .75rem; align-items: center; min-width: 0; }
.service-icon-picker img { flex: 0 0 40px; object-fit: contain; }
.service-icon-picker select { margin: 0; }
.service-editor-actions { display: flex; flex-wrap: wrap; gap: .6rem; margin-top: 1.5rem; }
.service-editor button { min-height: 44px; padding: .6rem .85rem; border: 1px solid var(--border); border-radius: 8px; background: var(--card-bg); color: var(--text); font: inherit; font-size: .9rem; cursor: pointer; }
.service-editor button:hover:not(:disabled) { background: var(--card-hover-bg); border-color: var(--accent); }
.service-editor button:disabled { opacity: .55; cursor: not-allowed; }
.service-editor { --editor-primary: #1d4ed8; }
.service-editor .service-editor-primary { background: var(--editor-primary); border-color: var(--editor-primary); color: #fff; font-weight: 650; }
.service-editor .service-editor-primary:hover:not(:disabled) { background: var(--editor-primary); filter: brightness(.93); }
.service-editor-secondary { display: flex; flex-wrap: wrap; gap: .4rem; margin-top: .85rem; }
.service-editor-secondary button { border-color: transparent; padding-left: 0; color: var(--muted); font-size: .82rem; }
.service-editor button[hidden] { display: none; }
@media (max-width: 600px) {
 .service-editor { width: 100%; max-width: none; max-height: calc(100dvh - 1rem); margin: auto 0 0; padding: 1.25rem max(1rem,env(safe-area-inset-right)) calc(1rem + env(safe-area-inset-bottom)) max(1rem,env(safe-area-inset-left)); border-radius: 16px 16px 0 0; }
 .service-editor-actions button { flex: 1 1 auto; }
}
`
