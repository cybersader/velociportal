package main

import "encoding/json"

// mcpToolResultSizeBound accounts for the frozen successful tool-result shape,
// not just its structured payload. This is pure budgeting, not a response writer
// or adapter. The future adapter must STILL buffer and enforce the actual cap.
func mcpToolResultSizeBound(result any) (int, error) {
	structured, err := json.Marshal(result)
	if err != nil {
		return 0, err
	}
	// A single oversized item must fail, never masquerade as an empty success.
	if len(structured) > mcpMaxResponseBytes {
		return mcpMaxResponseBytes + 1, nil
	}
	text, err := json.Marshal(string(structured))
	if err != nil {
		return 0, err
	}
	// mcpParseRequest bounds decoded string IDs to 256 UTF-8 bytes. Each byte
	// needs at most six JSON bytes (including retained raw Unicode escapes and
	// Marshal's HTML escaping); quotes add two. Accepted numeric IDs are at most
	// 17 bytes. The 16-KiB request cap cannot increase either accepted ID bound.
	// This reserves the larger ID without consuming/reparsing the actual body.
	const maxID = 6*256 + 2
	// Empty slots below are the ID, structured JSON and already-quoted text.
	const framing = `{"jsonrpc":"2.0","id":,"result":{"structuredContent":,"content":[{"type":"text","text":}],"isError":false}}`
	return len(framing) + maxID + len(structured) + len(text), nil
}
