package scan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Match Codex's envelope order and the expensive record types found in real
// stores, rather than benchmarking only cheap-to-ignore event_msg records.
func codexBenchmarkToolRecords() string {
	text, _ := json.Marshal(strings.Repeat("tool output with escaped quotes \" and braces {} ", 350))
	return `{"timestamp":"2026-10-12T12:00:00Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":` + string(text) + `}}` + "\n" +
		`{"timestamp":"2026-10-12T12:00:01Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":` + string(text) + `}}` + "\n" +
		`{"timestamp":"2026-10-12T12:00:02Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":` + string(text) + `}]}}` + "\n"
}

func BenchmarkCodexToolStore(b *testing.B) {
	root := b.TempDir()
	const files = 47
	records := codexBenchmarkToolRecords()
	padding := strings.Repeat(records, 363_000_000/files/len(records))
	var size int64
	for i := range files {
		body := codexTestMeta(map[string]any{"id": fmt.Sprint(i)}) + codexTestMessage("user", "find every session in this tool-heavy store")
		body += padding + codexTestRecord("turn_context", map[string]string{"model": "latest-model", "cwd": "/latest"})
		size += int64(len(body))
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("rollout-%02d.jsonl", i)), []byte(body), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	b.SetBytes(size)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sessions, err := Codex(root)
		if err != nil || len(sessions) != files {
			b.Fatalf("scan=%d sessions error=%v", len(sessions), err)
		}
		for _, s := range sessions {
			if s.Model != "latest-model" || s.Cwd != "/latest" {
				b.Fatal("lost metadata at the end of a tool-heavy rollout")
			}
		}
	}
}

func BenchmarkCodexHugeIgnoredRecord(b *testing.B) {
	body := codexTestMeta(nil) + codexTestMessage("user", "find the session after a huge ignored record")
	body += `{"type":"response_item","payload":{"type":"function_call_output","output":"` + strings.Repeat("x", 16<<20) + `"}}` + "\n"
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		meta, err := readCodex(strings.NewReader(body))
		if err != nil || meta.Title == "" {
			b.Fatalf("title=%q error=%v", meta.Title, err)
		}
	}
}
