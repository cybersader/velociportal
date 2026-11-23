package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Finite application adapters share the existing operation/store instances.
// Discovery is per-request; a descriptor is never authority.
type mcpPersonalResult struct {
	PersonalServicesState
	Projection []PersonalServiceStatus `json:"projection"`
}

type mcpTextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolResult struct {
	StructuredContent any              `json:"structuredContent"`
	Content           []mcpTextContent `json:"content"`
	IsError           bool             `json:"isError"`
}

type mcpApplicationTools struct {
	shared   *SharedIconOperations
	personal *PersonalServicesStore
	config   *MCPConfig
	cache    *Cache
	trusted  *net.IPNet
	interval time.Duration
	now      func() time.Time
}

// Local dispatch signal, not a new wire error. The concrete protocol adapter must
// map it to the incumbent -32602 / "Invalid params", not serialize err.Error().
var errMCPInvalidParams = errors.New("Invalid params")

// Capture fixed configuration and the SAME operation/store objects. Open nothing,
// infer no provider/mode, and never turn a mismatched supplied store into authority.
func newMCPApplicationTools(cfg *Config, shared *SharedIconOperations, cache *Cache, personal *PersonalServicesStore) *mcpApplicationTools {
	t := &mcpApplicationTools{cache: cache, now: time.Now}
	if cfg == nil {
		return t
	}
	t.interval = cfg.PollInterval
	if cfg.TrustedProxyCIDR != nil {
		t.trusted = &net.IPNet{IP: append(net.IP(nil), cfg.TrustedProxyCIDR.IP...), Mask: append(net.IPMask(nil), cfg.TrustedProxyCIDR.Mask...)}
	}
	if cfg.MCP != nil {
		copy := *cfg.MCP
		t.config = &copy
	}
	if shared != nil && shared.cache == cache && shared.personal == personal && shared.mode == cfg.PortalEditMode && shared.binding == cfg.metadataBinding() {
		t.shared = shared
	}
	p := cfg.PersonalServices
	if cfg.ControlPlane == controlPlaneTailscale && p != nil && personal != nil &&
		validStateNamespace(cfg.StateNamespace) && p.Namespace == cfg.StateNamespace && validPersonalFile(p.File) &&
		p.Origin == personal.config.Origin && p.Host == personal.config.Host &&
		p.File == personal.config.File && p.Namespace == personal.config.Namespace {
		t.personal = personal
	}
	return t
}

func (t *mcpApplicationTools) check(r *http.Request) error {
	if t == nil || t.config == nil {
		return errPersonalUnavailable
	}
	if checkMCPToolRequest(r, t.config, t.trusted) != nil {
		return errPersonalForbidden
	}
	return nil
}

// General shared eligibility does not inspect private state. Each application
// operation separately checks current ownership under the established lock order.
func (t *mcpApplicationTools) sharedCapability(r *http.Request) error {
	if !serviceMetadataV4RuntimeEnabled || t == nil || t.shared == nil {
		return errMetadataStale
	}
	o := t.shared
	identity, err := o.mcpTargetIdentity(r, "")
	if err != nil {
		return err
	}
	o.editor.mu.Lock()
	defer o.editor.mu.Unlock()
	at := o.now()
	if _, err := o.mcpTargetIdentity(r, identity.Login); err != nil {
		return err
	}
	if err := o.checkWriterLocked(); err != nil {
		return err
	}
	if o.editor.directory == nil || o.editor.lock == nil || !o.sharedTargetFresh(o.cache.Get(), at) {
		return errMetadataStale
	}
	return nil
}

func (t *mcpApplicationTools) List(r *http.Request) ([]any, error) {
	if err := t.check(r); err != nil {
		return nil, err
	}
	tools := []any{mcpIconsTool()}
	// The real read acquires the existing personal slot once, checks fresh owned
	// eligibility and discards private data. Discovery never allocates a collection.
	if _, _, err := t.personal.ReadMCP(r, t.config, t.cache, t.trusted, t.interval, t.now); err == nil {
		for _, name := range []string{"personal_read", "personal_create", "personal_update", "personal_delete"} {
			tools = append(tools, mcpApplicationTool(name))
		}
	}
	if t.sharedCapability(r) == nil {
		for _, name := range []string{"shared_targets_list", "shared_target_get", "shared_icon_set", "shared_icon_reset", "shared_order_set"} {
			tools = append(tools, mcpApplicationTool(name))
		}
		// Opt-in only: when disabled, discovery is byte-for-byte unchanged.
		if t.customIconsEnabled() {
			for _, name := range mcpCustomIconToolNames {
				tools = append(tools, mcpApplicationTool(name))
			}
		}
	}
	// Do not return even general descriptors if the actual request lost admission
	// during a blocking owned read. The operations recheck all authority on Call.
	if err := t.check(r); err != nil {
		return nil, err
	}
	return tools, nil
}

var mcpCustomIconToolNames = []string{"custom_icon_import", "custom_icon_get", "custom_icon_set"}

func mcpCustomIconTool(name string) bool {
	return name == "custom_icon_import" || name == "custom_icon_get" || name == "custom_icon_set"
}

func (t *mcpApplicationTools) customIconsEnabled() bool {
	return t != nil && t.shared.customIconsEnabled()
}

func (t *mcpApplicationTools) Call(r *http.Request, name string, arguments json.RawMessage) (mcpToolResult, error) {
	// Disabled custom tools are exactly as unknown as any other unknown name.
	if mcpCustomIconTool(name) && !t.customIconsEnabled() {
		return mcpToolResult{}, errMCPInvalidParams
	}
	fields, err := mcpApplicationArguments(name, arguments)
	if err != nil {
		return mcpToolResult{}, errMCPInvalidParams
	}
	if err := t.check(r); err != nil {
		return mcpApplicationResult(nil, err, false), nil
	}
	value := func(key string) string { v, _ := mcpString(fields[key]); return v }
	var data any
	var operationErr error
	mutation := false
	switch name {
	case "icons_list":
		data = mcpIconsResult()
	case "personal_read", "personal_create", "personal_update", "personal_delete":
		var state PersonalServicesState
		var snapshot *CacheData
		identity := *IdentityFromContext(r.Context())
		if name == "personal_read" {
			state, snapshot, operationErr = t.personal.ReadMCP(r, t.config, t.cache, t.trusted, t.interval, t.now)
		} else {
			mutation = true
			req := personalMCPMutation{StoreID: value("store_id"), Generation: value("generation"), Revision: value("revision"), Action: strings.TrimPrefix(name, "personal_"), ID: value("id"), Name: value("name"), URL: value("url"), Icon: value("icon")}
			state, snapshot, operationErr = t.personal.MutateMCP(r, req, t.config, t.cache, t.trusted, t.interval, t.now)
		}
		if operationErr == nil {
			// Use only the current snapshot returned by the operation, never marshal
			// it. The incumbent evaluator yields only own authored-ID/coarse status.
			_, projection := ProjectPersonalServices(&identity, snapshot, state, t.now(), t.interval)
			if t.check(r) != nil || IdentityFromContext(r.Context()).Login != identity.Login {
				operationErr = errPersonalForbidden
			} else {
				data = mcpPersonalResult{PersonalServicesState: state, Projection: projection}
			}
		}
	case "shared_targets_list", "shared_target_get", "shared_icon_set", "shared_icon_reset":
		mutation = name == "shared_icon_set" || name == "shared_icon_reset"
		if operationErr = t.sharedCapability(r); operationErr != nil {
			break
		}
		ref := sharedTargetRef{}
		if raw, present := fields["target"]; present {
			_ = json.Unmarshal(raw, &ref)
		} // exact keys already checked
		cas := sharedMutationCAS{Revision: value("revision"), TargetRevision: value("target_revision")}
		switch name {
		case "shared_targets_list":
			limit := 64
			if raw, present := fields["limit"]; present {
				limit, _ = strconv.Atoi(string(raw))
			}
			data, operationErr = t.shared.ListMCPTargets(r, sharedTargetListRequest{Kind: sharedTargetKind(value("kind")), Cursor: value("cursor"), Limit: limit})
		case "shared_target_get":
			data, operationErr = t.shared.GetMCPTarget(r, ref)
		case "shared_icon_set":
			operationErr = t.shared.SetMCPIcon(r, mcpSharedIconRequest{Target: ref, sharedMutationCAS: cas, Icon: value("icon")})
			data = map[string]string{"status": "saved"}
		case "shared_icon_reset":
			operationErr = t.shared.ResetMCPIcon(r, mcpSharedResetRequest{Target: ref, sharedMutationCAS: cas})
			data = map[string]string{"status": "saved"}
		}
	case "shared_order_set":
		mutation = true
		if operationErr = t.sharedCapability(r); operationErr != nil {
			break
		}
		entries, _ := mcpSharedOrderEntries(fields["entries"]) // already strictly validated
		var revision string
		revision, operationErr = t.shared.SetMCPOrder(r, mcpSharedOrderRequest{Revision: value("revision"), Entries: entries})
		data = map[string]string{"status": "saved", "revision": revision}
	case "custom_icon_import", "custom_icon_get", "custom_icon_set":
		mutation = name != "custom_icon_get"
		if operationErr = t.sharedCapability(r); operationErr != nil {
			break
		}
		ref := sharedTargetRef{}
		if raw, present := fields["target"]; present {
			_ = json.Unmarshal(raw, &ref)
		}
		switch name {
		case "custom_icon_import":
			var id string
			id, operationErr = t.shared.ImportMCPCustomIcon(r, mcpCustomIconImportRequest{PNG: value("png_base64"), Revision: value("revision")})
			data = map[string]string{"status": "saved", "custom_icon": id}
		case "custom_icon_get":
			data, operationErr = t.shared.GetMCPCustomIcon(r, ref)
		case "custom_icon_set":
			operationErr = t.shared.SetMCPCustomIcon(r, ref, sharedMutationCAS{Revision: value("revision"), TargetRevision: value("target_revision")}, value("custom_icon"))
			data = map[string]string{"status": "saved"}
		}
	}
	return mcpApplicationResult(data, operationErr, mutation), nil
}

// Fixed allowlists, not a schema interpreter. These same exact key sets describe
// the finite input schemas. Delete/reset cannot carry unused authored fields.
func mcpApplicationFields(name string) (required, optional []string, known bool) {
	switch name {
	case "icons_list", "personal_read":
		return []string{}, nil, true
	case "personal_create":
		return []string{"store_id", "generation", "revision", "name", "url"}, []string{"icon"}, true
	case "personal_update":
		return []string{"store_id", "generation", "revision", "name", "url", "id"}, []string{"icon"}, true
	case "personal_delete":
		return []string{"store_id", "generation", "revision", "id"}, nil, true
	case "shared_targets_list":
		return []string{"kind"}, []string{"cursor", "limit"}, true
	case "shared_target_get":
		return []string{"target"}, nil, true
	case "shared_icon_set":
		return []string{"target", "revision", "target_revision", "icon"}, nil, true
	case "shared_icon_reset":
		return []string{"target", "revision", "target_revision"}, nil, true
	case "shared_order_set":
		return []string{"revision", "entries"}, nil, true
	case "custom_icon_import":
		return []string{"png_base64", "revision"}, nil, true
	case "custom_icon_get":
		return []string{"target"}, nil, true
	case "custom_icon_set":
		return []string{"target", "revision", "target_revision", "custom_icon"}, nil, true
	default:
		return nil, nil, false
	}
}

func mcpApplicationArguments(name string, raw json.RawMessage) (map[string]json.RawMessage, error) {
	required, optional, known := mcpApplicationFields(name)
	if !known {
		return nil, errMCPInvalidParams
	}
	// Incumbent icons_list permits an omitted arguments object. Nothing else
	// receives that compatibility exception; null is never an empty object.
	if name == "icons_list" && len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if len(raw) == 0 || len(raw) > mcpMaxRequestBytes || mcpValidateJSON(raw) != nil || !personalJSONScalars(raw) {
		return nil, errMCPInvalidParams
	}
	fields, ok := mcpObject(raw)
	if !ok || !mcpFields(fields, append(append([]string{}, required...), optional...)...) {
		return nil, errMCPInvalidParams
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return nil, errMCPInvalidParams
		}
	}
	for key, raw := range fields {
		if key == "target" {
			if _, ok := mcpTargetArgument(raw); !ok {
				return nil, errMCPInvalidParams
			}
			continue
		}
		if key == "entries" {
			if _, err := mcpSharedOrderEntries(raw); err != nil {
				return nil, errMCPInvalidParams
			}
			continue
		}
		if key == "limit" {
			limit, err := strconv.Atoi(string(raw))
			if err != nil || limit < 1 || limit > 64 || strconv.Itoa(limit) != string(raw) {
				return nil, errMCPInvalidParams
			}
			continue
		}
		value, ok := mcpString(raw)
		if !ok {
			return nil, errMCPInvalidParams
		}
		switch key {
		case "kind":
			if _, err := sharedTargetListLimit(sharedTargetListRequest{Kind: sharedTargetKind(value)}); err != nil {
				return nil, errMCPInvalidParams
			}
		case "cursor":
			if len(value) > 256 || strings.TrimSpace(value) != value || containsControl(value) {
				return nil, errMCPInvalidParams
			}
		case "revision", "target_revision":
			if value == "" || len(value) > 256 || strings.TrimSpace(value) != value || containsControl(value) {
				return nil, errMCPInvalidParams
			}
		case "icon":
			if !validServiceIcon(value) && (value != "" || name == "shared_icon_set") {
				return nil, errMCPInvalidParams
			}
		case "png_base64":
			// Length bound before any base64 decode; content is validated later.
			if value == "" || len(value) > maxCustomIconInputBase64 {
				return nil, errMCPInvalidParams
			}
		case "custom_icon":
			if value != "" && !validCustomIconID(value) {
				return nil, errMCPInvalidParams
			}
		}
	}
	if name == "personal_create" || name == "personal_update" || name == "personal_delete" {
		value := func(key string) string { v, _ := mcpString(fields[key]); return v }
		if validatePersonalMCPMutation(personalMCPMutation{StoreID: value("store_id"), Generation: value("generation"), Revision: value("revision"), Action: strings.TrimPrefix(name, "personal_"), ID: value("id"), Name: value("name"), URL: value("url"), Icon: value("icon")}) != nil {
			return nil, errMCPInvalidParams
		}
	}
	return fields, nil
}

// Exact nested {kind, handle}; the handle carries no target information.
func mcpTargetArgument(raw json.RawMessage) (sharedTargetRef, bool) {
	target, ok := mcpObject(raw)
	if !ok || len(target) != 2 || !mcpFields(target, "kind", "handle") {
		return sharedTargetRef{}, false
	}
	kind, k := mcpString(target["kind"])
	handle, h := mcpString(target["handle"])
	ref := sharedTargetRef{Kind: sharedTargetKind(kind), Handle: handle}
	if !k || !h || validateSharedTargetRef(ref) != nil {
		return sharedTargetRef{}, false
	}
	return ref, true
}

// Strict shared_order_set entries: 1..64 objects with exact keys target plus
// at least one of order/category. Explicit null removes a field; an omitted
// field is unchanged. Duplicate targets are rejected, never merged.
func mcpSharedOrderEntries(raw json.RawMessage) ([]mcpSharedOrderEntry, error) {
	trimmed := bytes.TrimSpace(raw)
	var items []json.RawMessage
	if len(trimmed) == 0 || trimmed[0] != '[' || json.Unmarshal(trimmed, &items) != nil || len(items) < 1 || len(items) > maxSharedOrderEntries {
		return nil, errMCPInvalidParams
	}
	null := func(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }
	entries := make([]mcpSharedOrderEntry, 0, len(items))
	seen := make(map[sharedTargetRef]bool, len(items))
	for _, item := range items {
		object, ok := mcpObject(item)
		if !ok || !mcpFields(object, "target", "order", "category") {
			return nil, errMCPInvalidParams
		}
		ref, ok := mcpTargetArgument(object["target"])
		if !ok || seen[ref] {
			return nil, errMCPInvalidParams
		}
		seen[ref] = true
		entry := mcpSharedOrderEntry{Target: ref}
		if value, present := object["order"]; present {
			entry.SetOrder = true
			if !null(value) {
				order, err := strconv.Atoi(string(value))
				if err != nil || order < 0 || order > maxServiceMetadataOrder || strconv.Itoa(order) != string(value) {
					return nil, errMCPInvalidParams
				}
				entry.Order = &order
			}
		}
		if value, present := object["category"]; present {
			entry.SetCategory = true
			if !null(value) {
				category, ok := mcpString(value)
				if !ok {
					return nil, errMCPInvalidParams
				}
				if _, err := validateServiceMetadataCategory(category); err != nil {
					return nil, errMCPInvalidParams
				}
				entry.Category = &category
			}
		}
		if validateSharedOrderEntry(entry) != nil {
			return nil, errMCPInvalidParams
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func mcpApplicationErrorCode(err error) string {
	switch {
	case errors.Is(err, errPersonalForbidden), errors.Is(err, errMetadataForbidden), errors.Is(err, errPersonalCSRF), errors.Is(err, errMetadataCSRF):
		return "forbidden"
	case errors.Is(err, errMetadataNotVisible):
		return "not_found"
	case errors.Is(err, errPersonalConflict), errors.Is(err, errMetadataConflict):
		return "conflict"
	case errors.Is(err, errPersonalStorageUncertain), errors.Is(err, errMetadataUncertain):
		return "uncertain"
	case errors.Is(err, errPersonalCapacity), errors.Is(err, errCustomIconCapacity):
		return "capacity"
	case errors.Is(err, errPersonalInvalid), errors.Is(err, errMetadataInvalidFields):
		return "invalid_request"
	default:
		return "unavailable"
	}
}

// Freeze BOTH representations from the same encoded bytes, not two mutable views.
// The S4E budget reserves the worst accepted raw request ID plus the full envelope.
// An operation may have committed before encoding fails: uncertain is not rollback
// and the caller must read back, never replay a mutation using an old RPC/CAS ID.
func mcpApplicationResult(data any, operationErr error, mutation bool) mcpToolResult {
	isError := operationErr != nil
	if isError {
		data = map[string]string{"error": mcpApplicationErrorCode(operationErr)}
	}
	encoded, err := json.Marshal(data)
	if err == nil {
		var size int
		size, err = mcpToolResultSizeBound(json.RawMessage(encoded))
		if size > mcpMaxResponseBytes {
			err = errPersonalUnavailable
		}
	}
	if err != nil {
		code := "unavailable"
		if mutation && !isError {
			code = "uncertain"
		}
		encoded, _ = json.Marshal(map[string]string{"error": code})
		isError = true
	}
	return mcpToolResult{StructuredContent: json.RawMessage(encoded), Content: []mcpTextContent{{Type: "text", Text: string(encoded)}}, IsError: isError}
}

// Final tool buffer, called by production dispatch before headers. Rebuild from
// structured data so mismatched text cannot escape; enforce the actual envelope
// and raw accepted ID, not just an estimate.
func mcpApplicationResponse(id json.RawMessage, result mcpToolResult) ([]byte, error) {
	probe := append([]byte(`{"jsonrpc":"2.0","method":"tools/call","id":`), id...)
	probe = append(probe, '}')
	if len(probe) > mcpMaxRequestBytes || mcpValidateJSON(probe) != nil {
		return nil, errMCPInvalidParams
	}
	if _, ok := mcpParseRequest(probe); !ok {
		return nil, errMCPInvalidParams
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return nil, errPersonalUnavailable
	}
	// Always buffer complete data before any transport sees it. Return no bytes on
	// error; the protocol writer emits a bounded coarse error instead.
	result.StructuredContent = json.RawMessage(encoded)
	result.Content = []mcpTextContent{{Type: "text", Text: string(encoded)}}
	body, err := json.Marshal(mcpResponse{JSONRPC: "2.0", ID: id, Result: result})
	if err != nil || len(body) > mcpMaxResponseBytes {
		return nil, errPersonalUnavailable
	}
	return bytes.Clone(body), nil
}

// Finite schema constructors are presentation only; decoding above never trusts
// schema hints or encoding/json's case-insensitive struct matching.
func mcpApplicationObject(properties map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}
func mcpApplicationString(max int) map[string]any {
	return map[string]any{"type": "string", "maxLength": max}
}
func mcpApplicationIcon(empty bool) map[string]any {
	icons := append([]string{}, serviceIconIDs...)
	if empty {
		icons = append([]string{""}, icons...)
	}
	return map[string]any{"type": "string", "enum": icons}
}
func mcpApplicationTargetSchema() map[string]any {
	return mcpApplicationObject(map[string]any{
		"kind":   map[string]any{"type": "string", "enum": []string{"npm", "machine", "application"}},
		"handle": map[string]any{"type": "string", "minLength": 43, "maxLength": 43, "pattern": "^[A-Za-z0-9_-]{43}$"},
	}, "kind", "handle")
}
func mcpApplicationPersonalSchema() map[string]any {
	id := map[string]any{"type": "string", "pattern": "^ps_[0-9a-f]{32}$"}
	epoch := map[string]any{"type": "string", "pattern": "^[0-9a-f]{32}$"}
	return mcpApplicationObject(map[string]any{
		"durable": map[string]any{"type": "boolean", "const": true}, "store_id": epoch, "generation": epoch,
		"revision": map[string]any{"type": "string", "pattern": "^(0|[1-9][0-9]{0,19})$"}, "initialized": map[string]string{"type": "boolean"},
		"document": mcpApplicationObject(map[string]any{"version": map[string]any{"type": "integer", "const": 1},
			"entries": map[string]any{"type": "array", "maxItems": maxPersonalEntries, "items": mcpApplicationObject(map[string]any{
				"id": id, "name": mcpApplicationString(120), "url": mcpApplicationString(maxServiceMetadataURL), "icon": mcpApplicationIcon(false),
			}, "id", "name", "url")}}, "version", "entries"),
		"projection": map[string]any{"type": "array", "maxItems": maxPersonalEntries, "items": mcpApplicationObject(map[string]any{
			"id": id, "status": map[string]any{"type": "string", "enum": []string{"active", "unavailable"}},
		}, "id", "status")},
	}, "durable", "store_id", "generation", "revision", "initialized", "document", "projection")
}
func mcpApplicationSummaryProperties() map[string]any {
	p := map[string]any{"target": mcpApplicationTargetSchema(), "name": mcpApplicationString(120), "shared_icon": mcpApplicationIcon(true), "effective_icon": mcpApplicationIcon(true)}
	p["category"] = map[string]any{"type": "string", "minLength": 1, "maxLength": maxServiceMetadataCategory, "description": "NPM only: shared default category; omitted when none."}
	p["order"] = map[string]any{"type": "integer", "minimum": 0, "maximum": maxServiceMetadataOrder, "description": "NPM only: shared default order within its category; omitted when none."}
	return p
}
func mcpApplicationOrderEntriesSchema() map[string]any {
	item := mcpApplicationObject(map[string]any{
		"target":   mcpApplicationTargetSchema(),
		"order":    map[string]any{"type": []string{"integer", "null"}, "minimum": 0, "maximum": maxServiceMetadataOrder, "description": "New shared default order; null removes it; omit to leave unchanged."},
		"category": map[string]any{"type": []string{"string", "null"}, "minLength": 1, "maxLength": maxServiceMetadataCategory, "description": "New shared default category; null removes it; omit to leave unchanged."},
	}, "target")
	item["minProperties"] = 2
	return map[string]any{"type": "array", "minItems": 1, "maxItems": maxSharedOrderEntries, "items": item,
		"description": "Distinct NPM targets from shared_targets_list; all changes apply in one commit or none do."}
}
func mcpApplicationTool(name string) map[string]any {
	if name == "icons_list" {
		return mcpIconsTool()
	}
	required, optional, known := mcpApplicationFields(name)
	if !known {
		return nil
	}
	properties := map[string]any{}
	for _, key := range append(append([]string{}, required...), optional...) {
		switch key {
		case "store_id", "generation":
			properties[key] = map[string]any{"type": "string", "pattern": "^[0-9a-f]{32}$"}
		case "revision":
			if strings.HasPrefix(name, "personal_") {
				properties[key] = map[string]any{"type": "string", "pattern": "^(0|[1-9][0-9]{0,19})$"}
			} else {
				properties[key] = map[string]any{"type": "string", "minLength": 1, "maxLength": 256}
			}
		case "target_revision":
			properties[key] = map[string]any{"type": "string", "minLength": 1, "maxLength": 256}
		case "id":
			properties[key] = map[string]any{"type": "string", "pattern": "^ps_[0-9a-f]{32}$"}
		case "name":
			properties[key] = map[string]any{"type": "string", "minLength": 1, "maxLength": 120}
		case "url":
			properties[key] = map[string]any{"type": "string", "minLength": 1, "maxLength": maxServiceMetadataURL, "description": "Canonical HTTPS *.ts.net launch URL; no query, fragment or userinfo."}
		case "icon":
			properties[key] = mcpApplicationIcon(name != "shared_icon_set")
		case "target":
			properties[key] = mcpApplicationTargetSchema()
		case "entries":
			properties[key] = mcpApplicationOrderEntriesSchema()
		case "kind":
			properties[key] = map[string]any{"type": "string", "enum": []string{"npm", "machine", "application"}}
		case "cursor":
			properties[key] = mcpApplicationString(256)
		case "limit":
			properties[key] = map[string]any{"type": "integer", "minimum": 1, "maximum": 64, "default": 64}
		case "png_base64":
			properties[key] = map[string]any{"type": "string", "minLength": 1, "maxLength": maxCustomIconInputBase64, "description": "Standard padded base64 of one PNG, at most 8 KiB decoded and 128x128 pixels."}
		case "custom_icon":
			properties[key] = map[string]any{"type": "string", "pattern": "^(sha256:[0-9a-f]{64})?$", "description": "Stored custom icon ID; empty clears the assignment."}
		}
	}
	var output map[string]any
	var description string
	readOnly := false
	switch name {
	case "personal_read":
		output = mcpApplicationPersonalSchema()
		readOnly = true
		description = "Read only your confirmed authored personal services and current coarse activity. Requires fresh ownership; inactive records remain manageable."
	case "personal_create", "personal_update", "personal_delete":
		output = mcpApplicationPersonalSchema()
		description = "Change only your durable authored personal services using current collection CAS. A lost or uncertain response requires read-back and review, never replay. Does not grant access."
	case "shared_targets_list":
		output = mcpApplicationObject(map[string]any{"targets": map[string]any{"type": "array", "maxItems": 64, "items": mcpApplicationObject(mcpApplicationSummaryProperties(), "target", "name", "shared_icon", "effective_icon")}, "next_cursor": mcpApplicationString(256)}, "targets")
		readOnly = true
		description = "List one kind of currently eligible shared presentation targets. No totals or global mappings; application targets require your own active entry."
	case "shared_target_get":
		p := mcpApplicationSummaryProperties()
		p["revision"] = mcpApplicationString(256)
		p["target_revision"] = mcpApplicationString(256)
		output = mcpApplicationObject(p, "target", "name", "shared_icon", "effective_icon", "revision", "target_revision")
		readOnly = true
		description = "Read one currently eligible shared presentation target and opaque CAS. The handle does not grant authority."
	case "shared_icon_set", "shared_icon_reset":
		output = mcpApplicationObject(map[string]any{"status": map[string]any{"type": "string", "const": "saved"}}, "status")
		description = "Change only the selected shared finite icon using current document and target CAS. Preserves other presentation fields; never grants access. Lost responses require read-back, not replay."
	case "shared_order_set":
		output = mcpApplicationObject(map[string]any{"status": map[string]any{"type": "string", "const": "saved"}, "revision": mcpApplicationString(256)}, "status", "revision")
		description = "Set or remove the shared default category/order of up to 64 visible NPM targets in one commit using the current document revision. Returns the new revision. Browser-local order may still override it in a given browser; never grants access. Lost responses require read-back, not replay."
	case "custom_icon_import":
		output = mcpApplicationObject(map[string]any{"status": map[string]any{"type": "string", "const": "saved"}, "custom_icon": map[string]any{"type": "string", "pattern": "^sha256:[0-9a-f]{64}$"}}, "status", "custom_icon")
		description = "Store one small PNG as a reusable shared custom icon and return its content ID. Re-encoded without metadata; identical images return the same ID. Grants no card visibility."
	case "custom_icon_get":
		output = mcpApplicationObject(map[string]any{"target": mcpApplicationTargetSchema(), "name": mcpApplicationString(120),
			"custom_icon": map[string]any{"type": "string", "pattern": "^(sha256:[0-9a-f]{64})?$"}, "revision": mcpApplicationString(256), "target_revision": mcpApplicationString(256)},
			"target", "name", "custom_icon", "revision", "target_revision")
		readOnly = true
		description = "Read one currently visible NPM target's custom icon ID and opaque CAS. NPM targets only."
	case "custom_icon_set":
		output = mcpApplicationObject(map[string]any{"status": map[string]any{"type": "string", "const": "saved"}}, "status")
		description = "Assign a stored custom icon ID to one visible NPM target, or clear it with an empty ID, using document and target CAS. Lost responses require read-back, not replay."
	}
	return map[string]any{"name": name, "description": description, "inputSchema": mcpApplicationObject(properties, required...), "outputSchema": output,
		"annotations": map[string]bool{"readOnlyHint": readOnly, "destructiveHint": !readOnly, "idempotentHint": readOnly, "openWorldHint": false}}
}
