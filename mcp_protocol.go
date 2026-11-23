package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This is deliberately not a general JSON-RPC framework: one request envelope,
// lifecycle/discovery methods and nine fixed application tools.
type mcpRequest struct {
	ID     json.RawMessage
	Method string
	Params map[string]json.RawMessage
}

type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpRPCError    `json:"error,omitempty"`
}

func mcpWrite(w http.ResponseWriter, status int, id json.RawMessage, result any, code int, message string) {
	if id == nil {
		id = json.RawMessage("null")
	}
	response := mcpResponse{JSONRPC: "2.0", ID: id, Result: result}
	if code != 0 {
		response.Result = nil
		response.Error = &mcpRPCError{Code: code, Message: message}
	}
	body, err := json.Marshal(response)
	if err != nil || len(body) > mcpMaxResponseBytes {
		// Buffer before headers: never acknowledge an oversized/partial result.
		body = []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"Internal error"}}`)
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// Token-walk before decoding maps: encoding/json alone silently accepts duplicate
// keys and case aliases. Bound every container, not only the expected envelope.
func mcpValidateJSON(body []byte) error {
	if !utf8.Valid(body) || !json.Valid(body) {
		return errors.New("invalid JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, container := token.(json.Delim)
		if !container {
			return nil
		}
		if depth >= mcpMaxJSONDepth {
			return errors.New("JSON too deep")
		}
		keys := map[string]bool{}
		for decoder.More() {
			if delim == '{' {
				token, err = decoder.Token()
				if err != nil {
					return err
				}
				key, ok := token.(string)
				if !ok || keys[strings.ToLower(key)] {
					return errors.New("duplicate JSON field")
				}
				keys[strings.ToLower(key)] = true
			}
			if err := walk(depth + 1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	}
	if err := walk(0); err != nil {
		return err
	}
	_, err := decoder.Token()
	if err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func mcpObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return nil, false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, false
	}
	return object, true
}

func mcpString(raw json.RawMessage) (string, bool) {
	var value string
	if len(raw) == 0 || bytes.TrimSpace(raw)[0] != '"' || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func mcpFields(object map[string]json.RawMessage, allowed ...string) bool {
	for key := range object {
		if !mcpContains(allowed, key) {
			return false
		}
	}
	if raw, present := object["_meta"]; present {
		_, ok := mcpObject(raw)
		return ok
	}
	return true
}

func mcpParseRequest(body []byte) (mcpRequest, bool) {
	object, ok := mcpObject(body)
	if !ok || !mcpFields(object, "jsonrpc", "id", "method", "params") {
		return mcpRequest{}, false
	}
	version, _ := mcpString(object["jsonrpc"])
	method, methodOK := mcpString(object["method"])
	if version != "2.0" || !methodOK || method == "" || len(method) > 128 || containsControl(method) {
		return mcpRequest{}, false
	}
	request := mcpRequest{ID: object["id"], Method: method, Params: map[string]json.RawMessage{}}
	if request.ID != nil {
		if id, stringID := mcpString(request.ID); stringID {
			if len(id) > 256 || containsControl(id) {
				return mcpRequest{}, false
			}
		} else {
			number, err := strconv.ParseInt(string(request.ID), 10, 64)
			if err != nil || number < -9007199254740991 || number > 9007199254740991 {
				return mcpRequest{}, false
			}
		}
	}
	if raw, present := object["params"]; present {
		request.Params, ok = mcpObject(raw)
		if !ok {
			return mcpRequest{}, false
		}
	}
	return request, true
}

// Keep the actual admitted request available to later concrete tool operations;
// initialization and discovery never turn it into retained session authority.
func (h *mcpHandler) dispatch(w http.ResponseWriter, r *http.Request, body []byte, versionPresent bool) {
	if !utf8.Valid(body) || !json.Valid(body) {
		mcpWrite(w, http.StatusBadRequest, nil, nil, -32700, "Parse error")
		return
	}
	if mcpValidateJSON(body) != nil {
		mcpWrite(w, http.StatusBadRequest, nil, nil, -32600, "Invalid Request")
		return
	}
	request, ok := mcpParseRequest(body)
	if !ok {
		mcpWrite(w, http.StatusBadRequest, nil, nil, -32600, "Invalid Request")
		return
	}
	// Missing headers default to 2025-03-26 under Streamable HTTP compatibility
	// rules. We do not implement that version. Only initialize may omit it;
	// an unsupported version offered in initialize params is negotiated below.
	if !versionPresent && request.Method != "initialize" {
		http.Error(w, "Unsupported protocol version", http.StatusBadRequest)
		return
	}
	if request.ID == nil {
		// JSON-RPC notifications never receive RPC responses. Unknown notifications
		// (including request methods sent without IDs) are ignored, never invoked.
		// There is no initialized/session state to mutate in this stateless server.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	invalidParams := func() { mcpWrite(w, http.StatusOK, request.ID, nil, -32602, "Invalid params") }
	var result any
	switch request.Method {
	case "initialize":
		if !mcpFields(request.Params, "protocolVersion", "capabilities", "clientInfo", "_meta") {
			invalidParams()
			return
		}
		offer, validOffer := mcpString(request.Params["protocolVersion"])
		_, capsOK := mcpObject(request.Params["capabilities"])
		client, clientOK := mcpObject(request.Params["clientInfo"])
		name, nameOK := mcpString(client["name"])
		version, versionOK := mcpString(client["version"])
		if !validOffer || offer == "" || len(offer) > 64 || !capsOK || !clientOK || !nameOK || name == "" || len(name) > 256 || !versionOK || version == "" || len(version) > 256 {
			invalidParams()
			return
		}
		result = map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": "velociportal-readonly-prototype", "version": "0.1.0"},
		}
	case "ping":
		if !mcpFields(request.Params, "_meta") {
			invalidParams()
			return
		}
		result = map[string]any{}
	case "tools/list":
		if !mcpFields(request.Params, "_meta") {
			invalidParams()
			return
		}
		tools, err := h.tools.List(r)
		if err != nil {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		result = map[string]any{"tools": tools}
	case "tools/call":
		name, valid := mcpString(request.Params["name"])
		if !mcpFields(request.Params, "name", "arguments", "_meta") || !valid {
			invalidParams()
			return
		}
		tool, err := h.tools.Call(r, name, request.Params["arguments"])
		if errors.Is(err, errMCPInvalidParams) {
			invalidParams()
			return
		}
		if err != nil {
			tool = mcpApplicationResult(nil, errPersonalUnavailable, false)
		}
		mcpWriteTool(w, request.ID, tool, name)
		return
	default:
		mcpWrite(w, http.StatusOK, request.ID, nil, -32601, "Method not found")
		return
	}
	mcpWrite(w, http.StatusOK, request.ID, result, 0, "")
}

// Dispatch holds its route slot until this complete response (and any synchronous
// commit) finishes. Never send headers before encoding both copies and the ID.
func mcpWriteTool(w http.ResponseWriter, id json.RawMessage, result mcpToolResult, name string) {
	body, err := mcpApplicationResponse(id, result)
	if err != nil {
		failure := error(errPersonalUnavailable)
		if !result.IsError && (name == "personal_create" || name == "personal_update" || name == "personal_delete" || name == "shared_icon_set" || name == "shared_icon_reset" || name == "shared_order_set" || name == "custom_icon_import" || name == "custom_icon_set") {
			failure = errMetadataUncertain
		}
		body, err = mcpApplicationResponse(id, mcpApplicationResult(nil, failure, false))
		if err != nil {
			mcpWrite(w, http.StatusInternalServerError, nil, nil, -32603, "Internal error")
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func mcpIconsTool() map[string]any {
	emptyObject := map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
	return map[string]any{
		"name":        "icons_list",
		"description": "List the finite embedded presentation icon IDs and labels. No private data or network access.",
		"inputSchema": emptyObject,
		"outputSchema": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"icons"},
			"properties": map[string]any{"icons": map[string]any{
				"type": "array", "maxItems": len(serviceIcons),
				"items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"id", "label"},
					"properties": map[string]any{"id": map[string]string{"type": "string"}, "label": map[string]string{"type": "string"}}},
			}},
		},
		"annotations": map[string]bool{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
	}
}

func mcpIconsResult() map[string]any {
	icons := make([]map[string]string, len(serviceIcons))
	for i, icon := range serviceIcons {
		icons[i] = map[string]string{"id": icon.ID, "label": icon.Label}
	}
	return map[string]any{"icons": icons}
}
