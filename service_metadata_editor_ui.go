package main

import (
	"fmt"
	"html"
	"strings"
)

func renderServiceEditor(enabled bool) string {
	if !enabled {
		return ""
	}
	var choices strings.Builder
	choices.WriteString(`<option value="" data-icon-path="/static/service-icons/generic.svg">Default (generic service)</option>`)
	for _, icon := range serviceIcons {
		fmt.Fprintf(&choices, `<option value="%s" data-icon-path="%s">%s</option>`, icon.ID, icon.Path, html.EscapeString(icon.Label))
	}
	return `<p class="service-editor-announcement" id="service-editor-announcement" role="status" aria-live="polite"></p>
<dialog id="service-editor" class="service-editor" aria-labelledby="service-editor-title" aria-describedby="service-editor-help">
<form id="service-editor-form">
<h2 id="service-editor-title" tabindex="-1" autofocus>Edit service</h2>
<p id="service-editor-help">Changes are shared with everyone who can already see this service. They do not change access or backend checks.</p>
<p id="service-editor-status" role="status" aria-live="polite">Loading current values…</p>
<fieldset id="service-editor-fields" disabled>
<label for="service-editor-name">Display name</label>
<input id="service-editor-name" type="text" maxlength="120" autocomplete="off" spellcheck="false" aria-describedby="service-editor-defaults">
<label for="service-editor-url">Link URL</label>
<input id="service-editor-url" type="text" inputmode="url" maxlength="2048" autocomplete="off" spellcheck="false" autocapitalize="none" aria-describedby="service-editor-defaults">
<p id="service-editor-defaults" class="service-editor-hint">Leave fields empty to use the current defaults.</p>
<label for="service-editor-icon">Local icon</label>
<div class="service-icon-picker"><img id="service-editor-preview" src="/static/service-icons/generic.svg" width="40" height="40" alt=""><select id="service-editor-icon">` + choices.String() + `</select></div>
</fieldset>
<div class="service-editor-actions"><button id="service-editor-save" class="service-editor-primary" type="submit" disabled>Save for everyone</button><button id="service-editor-cancel" type="button">Cancel</button></div>
<div class="service-editor-secondary"><button id="service-editor-reset" type="button" disabled>Reset name/link/icon</button><button id="service-editor-reload" type="button" hidden>Reload for review</button></div>
</form>
</dialog>
<script src="/static/service-editor.js" defer></script>`
}

const serviceEditorCSS = `
.service-icon { display: block; width: 32px; height: 32px; object-fit: contain; }
.card-editable { padding: 0; gap: 0; overflow: hidden; }
.service-main { display: flex; flex: 1; flex-direction: column; gap: .55rem; min-width: 0; min-height: 44px; padding: 1rem 1.1rem; color: inherit; text-decoration: none; }
a.service-main:hover, a.service-main:focus-visible { background: var(--card-hover-bg); }
.service-edit-button { min-height: 44px; padding: .5rem 1.1rem; border: 0; border-top: 1px solid var(--border); background: var(--card-bg); color: var(--muted); text-align: left; font: inherit; font-size: .82rem; cursor: pointer; }
.service-edit-button:hover { background: var(--card-hover-bg); color: var(--text); }
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
