package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Independent literal future wire shape, not the budgeting helper or a new
// production adapter. Exercise the incumbent envelope encoder and ID validator.
func encodedSharedPage(t *testing.T, page sharedTargetPage, id json.RawMessage) []byte {
	t.Helper()
	payload, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]any{"structuredContent": page, "content": []any{map[string]string{"type": "text", "text": string(payload)}}, "isError": false}
	wire, err := json.Marshal(mcpResponse{JSONRPC: "2.0", ID: id, Result: result})
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func TestMCPResultSizeConservativeCompleteDualEncoding(t *testing.T) {
	ids := []string{`"` + strings.Repeat("<", 256) + `"`, `"` + strings.Repeat(string([]byte{92})+"u003c", 256) + `"`, `"` + strings.Repeat(`\/`, 256) + `"`, `"` + strings.Repeat(`\"`, 256) + `"`, `"` + strings.Repeat("界", 85) + `"`, `"` + strings.Repeat(`😀`, 64) + `"`, `"` + strings.Repeat(`\ud800`, 85) + `"`, "-9007199254740991", "9007199254740991"}
	for _, name := range []string{strings.Repeat("<", 120), strings.Repeat("界", 120), strings.Repeat("😀", 120), strings.Repeat(" ", 120), strings.Repeat("\x00\"\\", 40), ""} {
		page := sharedTargetPage{Targets: []sharedTargetSummary{}, NextCursor: strings.Repeat("A", 87)}
		for i := 0; i < 64; i++ {
			page.Targets = append(page.Targets, sharedTargetSummary{Target: sharedTargetRef{Kind: sharedTargetApplication, Handle: strings.Repeat("A", 43)}, Name: name, SharedIcon: "adguard-home", EffectiveIcon: "adguard-home"})
		}
		bound, err := mcpToolResultSizeBound(page)
		if err != nil || bound > mcpMaxResponseBytes {
			t.Fatal("bounded maximum page", bound, err)
		}
		for _, id := range ids {
			body := []byte(`{"jsonrpc":"2.0","id":` + id + `,"method":"tools/call","params":{"name":"shared_targets_list","arguments":{"kind":"application"}}}`)
			if len(body) > mcpMaxRequestBytes || mcpValidateJSON(body) != nil {
				t.Fatal("invalid test envelope")
			}
			req, ok := mcpParseRequest(body)
			if !ok {
				t.Fatal("ID not accepted by incumbent", id)
			}
			wire := encodedSharedPage(t, page, req.ID)
			if len(wire) > bound || len(wire) > 256*1024 {
				t.Fatalf("complete encoded response %d > bound %d", len(wire), bound)
			}
			var response struct {
				Result struct {
					StructuredContent sharedTargetPage `json:"structuredContent"`
					Content           []struct {
						Text string `json:"text"`
					} `json:"content"`
				}
			}
			if json.Unmarshal(wire, &response) != nil || len(response.Result.Content) != 1 {
				t.Fatal("wire shape")
			}
			text, _ := json.Marshal(response.Result.StructuredContent)
			if string(text) != response.Result.Content[0].Text {
				t.Fatal("dual representations differ")
			}
		}
	}
	if _, err := mcpToolResultSizeBound(make(chan int)); err == nil {
		t.Fatal("marshal error ignored")
	}
}
