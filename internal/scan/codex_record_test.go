package scan

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCodexEscapedEnvelopeFields(t *testing.T) {
	body := codexTestMeta(nil)
	body += `{ "t\u0079pe" : "respon\u0073e_item", "payload" : { "type" : "message", "r\u006fle" : "user", "content" : "find this session with escaped JSON keys" } }` + "\n"
	body += `{"type":"turn_context","payload":{"model":"latest-model","cwd":"/latest"}}` + "\n"
	sessions := codexTestScan(t, body)
	if len(sessions) != 1 || sessions[0].Title != "find this session with escaped JSON keys" || sessions[0].Model != "latest-model" {
		t.Fatalf("lost an escaped envelope: %+v", sessions)
	}
}

func TestCodexIgnoresMarkersInsideToolOutput(t *testing.T) {
	output := `{"type":"session_meta","payload":{"id":"wrong","cwd":"/wrong"}} response_item message user`
	body := codexTestMeta(nil)
	body += codexTestRecord("response_item", map[string]any{"type": "function_call_output", "output": output})
	body += codexTestMessage("user", "find the genuine request after misleading tool output")
	sessions := codexTestScan(t, body)
	if len(sessions) != 1 || sessions[0].ID != "session" || sessions[0].Title != "find the genuine request after misleading tool output" {
		t.Fatalf("tool output changed session discovery: %+v", sessions)
	}
}

// Generate valid envelopes with unique fields and arbitrary escaped/nested
// content. Every prefix must retain relevant records, even when classification
// needs fields that have not arrived yet.
func FuzzCodexRelevantPrefixes(f *testing.F) {
	f.Add("fix the parser", `quoted \" braces { }`, false, uint16(64))
	f.Add("preserve metadata", "nested\\\"text", true, uint16(128))
	f.Fuzz(func(t *testing.T, text, padding string, reversed bool, cut uint16) {
		content, _ := json.Marshal(text)
		extra, _ := json.Marshal(map[string]any{"array": []any{padding, nil, true, map[string]string{"type": "function_call"}}})
		for _, kind := range []string{"session_meta", "turn_context", "response_item"} {
			payload := fmt.Sprintf(`{"type":"message","role":"assistant","content":%s,"extra":%s}`, content, extra)
			if reversed {
				payload = fmt.Sprintf(`{"extra":%s,"content":%s,"role":"assistant","type":"message"}`, extra, content)
			}
			record := fmt.Sprintf(`{"type":%q,"payload":%s}`, kind, payload)
			if reversed {
				record = fmt.Sprintf(`{"payload":%s,"type":%q}`, payload, kind)
			}
			prefix := []byte(record[:int(cut)%(len(record)+1)])
			if _, discard := codexDiscard(prefix, false); discard {
				t.Fatalf("discarded relevant %s prefix %q", kind, prefix)
			}
			if _, discard := codexDiscard([]byte(record), false); discard {
				t.Fatalf("discarded complete relevant %s record", kind)
			}
		}
	})
}

func TestCodexDiscardLargeToolRecordFromPrefix(t *testing.T) {
	prefix := `{"timestamp":"2026-10-12T12:00:00Z","type":"response_item","payload":{"type":"function_call_output","output":"` + strings.Repeat("x", 64<<10)
	if kind, discard := codexDiscard([]byte(prefix), false); kind != "response_item" || !discard {
		t.Fatalf("large tool prefix kind=%q discard=%v", kind, discard)
	}
}
