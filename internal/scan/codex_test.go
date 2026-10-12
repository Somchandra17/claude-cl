package scan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexParsesSessionMetaAndUserPrompt(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "2026", "09", "04")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"session_meta","payload":{"id":"abc-1","cwd":"/repo"}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions\nskip me"}]}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the flaky auth test please"}]}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"looking at the test now"}]}}
`
	path := filepath.Join(dir, "rollout-2026-09-04T01-00-00-abc-1.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Codex(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1", len(got))
	}
	s := got[0]
	if s.ID != "abc-1" || s.Cwd != "/repo" {
		t.Errorf("id/cwd = %s %s", s.ID, s.Cwd)
	}
	if s.Title != "fix the flaky auth test please" {
		t.Errorf("title = %q, want the human prompt not AGENTS.md", s.Title)
	}
	if s.Tool != "codex" {
		t.Errorf("tool = %q", s.Tool)
	}
}

func codexTestRecord(kind string, payload any) string {
	b, _ := json.Marshal(map[string]any{"type": kind, "payload": payload})
	return string(b) + "\n"
}

func codexTestMeta(extra map[string]any) string {
	payload := map[string]any{"id": "session", "cwd": "/repo"}
	for key, value := range extra {
		payload[key] = value
	}
	return codexTestRecord("session_meta", payload)
}

func codexTestMessage(role, text string) string {
	return codexTestRecord("response_item", map[string]any{
		"type": "message", "role": role,
		"content": []map[string]string{{"type": "input_text", "text": text}},
	})
}

func codexTestScan(t *testing.T, body string) []Session {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "rollout-test.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions, err := Codex(root)
	if err != nil {
		t.Fatal(err)
	}
	return sessions
}

func TestCodexContextAndUserMarkup(t *testing.T) {
	prompt := "fix the actual session search bug"
	cases := []struct{ name, context, request string }{
		{"environment", "<environment_context>Current directory: /repo</environment_context>", prompt},
		{"agents", "# AGENTS.md instructions\nThese are repository instructions", prompt},
		{"user instructions", "<user_instructions>Follow these injected instructions</user_instructions>", prompt},
		{"page context", "<external_codex_apps_open_page>{\"page_id\":null}</external_codex_apps_open_page>", prompt},
		{"skill manual", "<skill><name>session-tools</name>Follow this lengthy injected skill manual</skill>", prompt},
		{"context attributes", "<environment_context source=\"desktop\">Current directory: /repo</environment_context>", prompt},
		{"mixed context", "", "<environment_context>Current directory: /repo</environment_context>\n" + prompt},
		{"multiple contexts", "", "<environment_context>Current directory: /repo</environment_context>\n<user_instructions>Injected instructions</user_instructions>\n" + prompt},
		{"user markup", "", "<task>" + prompt + "</task>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := codexTestMeta(nil)
			if tc.context != "" {
				body += codexTestMessage("user", tc.context)
			}
			body += codexTestMessage("user", tc.request)
			sessions := codexTestScan(t, body)
			if len(sessions) != 1 {
				t.Fatalf("listed %d sessions, want 1", len(sessions))
			}
			if sessions[0].Title != prompt {
				t.Errorf("title = %q, want %q", sessions[0].Title, prompt)
			}
			if len(sessions[0].Preview) != 1 || sessions[0].Preview[0].Text != prompt {
				t.Errorf("preview = %+v, want only the actual request", sessions[0].Preview)
			}
		})
	}
}

func TestCodexRecoversRecordsBeyondWindows(t *testing.T) {
	prompt := "find this session regardless of its opening record sizes"
	padding := codexTestRecord("event_msg", map[string]string{"type": "ignored", "text": strings.Repeat("x", 3<<20)})
	cases := []struct{ name, body string }{
		{"late prompt", codexTestMeta(nil) + padding + codexTestMessage("user", prompt)},
		{"large metadata", codexTestMeta(map[string]any{"base_instructions": map[string]string{"text": strings.Repeat("x", 3<<20)}}) + codexTestMessage("user", prompt) + padding},
		{"late context", codexTestMeta(nil) + codexTestMessage("user", prompt) + padding + codexTestRecord("turn_context", map[string]string{"model": "latest-model", "cwd": "/current"}) + padding},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessions := codexTestScan(t, tc.body)
			if len(sessions) != 1 || sessions[0].Title != prompt {
				t.Fatalf("listed %d sessions; want the late or oversized session", len(sessions))
			}
			if tc.name == "late context" && (sessions[0].Model != "latest-model" || sessions[0].Cwd != "/current") {
				t.Errorf("model/cwd = %q/%q, want latest-model/current", sessions[0].Model, sessions[0].Cwd)
			}
		})
	}
}

func TestCodexMetadataPrecedenceAndPreview(t *testing.T) {
	body := codexTestMeta(map[string]any{"forked_from_id": "parent", "git": map[string]string{"branch": "feature/search"}})
	body += codexTestMessage("user", "fix the original session search request")
	body += codexTestRecord("session_meta", map[string]string{"id": "inherited", "cwd": "/inherited", "forked_from_id": "wrong-parent"})
	body += codexTestRecord("turn_context", map[string]string{"model": "old-model", "cwd": "/old"})
	body += codexTestRecord("turn_context", map[string]string{"model": "current-model", "cwd": "/current"})
	for range 8 {
		body += codexTestMessage("assistant", "recent assistant response")
	}
	body += codexTestRecord("response_item", map[string]any{"type": "message", "role": "assistant", "model": "legacy-model", "content": "final assistant response"})
	body += `{"type":"response_item","payload":` // incomplete write from an active session
	sessions := codexTestScan(t, body)
	if len(sessions) != 1 {
		t.Fatalf("listed %d sessions, want 1", len(sessions))
	}
	s := sessions[0]
	if s.ID != "session" || s.ParentID != "parent" || s.Branch != "feature/search" {
		t.Errorf("identity/fork/branch = %q/%q/%q", s.ID, s.ParentID, s.Branch)
	}
	if s.Model != "current-model" || s.Cwd != "/current" {
		t.Errorf("model/cwd = %q/%q, want latest turn context", s.Model, s.Cwd)
	}
	if len(s.Preview) != previewTurns || s.Preview[len(s.Preview)-1].Text != "final assistant response" {
		t.Errorf("preview = %+v, want bounded recent complete messages", s.Preview)
	}
}

func TestCodexSkipsNonUserSources(t *testing.T) {
	for _, source := range []any{
		map[string]any{"subagent": "review"},
		map[string]any{"subagent": "compact"},
		map[string]any{"subagent": "memory_consolidation"},
		map[string]any{"subagent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": "parent", "depth": 1}}},
		map[string]any{"subagent": map[string]any{"other": "future-kind"}},
		map[string]any{"internal": "future-kind"},
		"subagent",
		"subagent_thread_spawn",
	} {
		b, _ := json.Marshal(source)
		t.Run(string(b), func(t *testing.T) {
			body := codexTestMeta(map[string]any{"source": source}) + codexTestMeta(map[string]any{"id": "inherited-parent", "source": "vscode"}) + codexTestMessage("user", "an inherited parent request must not title a subagent")
			if sessions := codexTestScan(t, body); len(sessions) != 0 {
				t.Errorf("listed %d non-user sessions, want none", len(sessions))
			}
		})
	}
}

func TestCodexWithoutHumanPrompt(t *testing.T) {
	body := codexTestMeta(nil) + codexTestMessage("user", "<environment_context>Only injected context</environment_context>") + codexTestMessage("assistant", "assistant output alone is not a human request")
	if sessions := codexTestScan(t, body); len(sessions) != 0 {
		t.Errorf("listed %d sessions without a human prompt", len(sessions))
	}
}

func TestCodexCompleteFinalRecordWithoutNewline(t *testing.T) {
	body := codexTestMeta(nil) + strings.TrimSuffix(codexTestMessage("user", "a complete final record need not end with a newline"), "\n")
	if sessions := codexTestScan(t, body); len(sessions) != 1 {
		t.Errorf("listed %d sessions, want 1", len(sessions))
	}
}

func TestCodexMixedTextBlocksAndLegacyModel(t *testing.T) {
	message := codexTestRecord("response_item", map[string]any{
		"type": "message", "role": "user", "model": "legacy-model",
		"content": []map[string]string{
			{"type": "input_text", "text": "include every"},
			{"type": "text", "text": "human text"},
			{"type": "output_text", "text": "block in order"},
			{"type": "image", "text": "ignore non-text blocks"},
		},
	})
	sessions := codexTestScan(t, codexTestMeta(nil)+message)
	if len(sessions) != 1 || sessions[0].Title != "include every human text block in order" || sessions[0].Model != "legacy-model" {
		t.Errorf("sessions = %+v, want all text blocks and the legacy model fallback", sessions)
	}
}

func TestCodexHumanRequestAboutAgentsFile(t *testing.T) {
	for _, prompt := range []string{"AGENTS.md needs updated guidance for session discovery", "AGENTS.md instructions need updated guidance for session discovery", "# AGENTS.md\nupdate the guidance for session discovery"} {
		sessions := codexTestScan(t, codexTestMeta(nil)+codexTestMessage("user", prompt))
		if len(sessions) != 1 || sessions[0].Title != strings.Join(strings.Fields(prompt), " ") {
			t.Errorf("request about AGENTS.md was discarded: %+v", sessions)
		}
	}
}

func TestCodexLiteralContextTagsInHumanRequests(t *testing.T) {
	cases := []struct{ prompt, title string }{
		{"please fix the literal <environment_context> tag parsing in the reader", "please fix the literal tag parsing in the reader"},
		{"explain how <environment_context>example values</environment_context> appear in user text", "explain how example values appear in user text"},
		{"<environment_context> tag handling needs a fix", "tag handling needs a fix"},
	}
	for _, tc := range cases {
		sessions := codexTestScan(t, codexTestMeta(nil)+codexTestMessage("user", tc.prompt))
		if len(sessions) != 1 || sessions[0].Title != tc.title {
			t.Errorf("request %q: sessions = %+v, want preserved searchable intent", tc.prompt, sessions)
		}
	}
}

func TestCodexWhitespaceAndMalformedRecords(t *testing.T) {
	body := "not json\n" + `{ "type" : "session_meta", "payload" : { "id" : "spaced", "cwd" : "/repo" } }` + "\n"
	body += `{ "type" : "response_item", "payload" : { "type" : "message", "role" : "user", "content" : "find this session with spaced JSON fields" } }` + "\n"
	sessions := codexTestScan(t, body)
	if len(sessions) != 1 || sessions[0].ID != "spaced" {
		t.Errorf("sessions = %+v, want the valid spaced records", sessions)
	}
}

func BenchmarkCodexLargeRollout(b *testing.B) {
	root := b.TempDir()
	body := codexTestMeta(nil) + codexTestMessage("user", "find the benchmark session in a large rollout")
	padding := codexTestRecord("event_msg", map[string]string{"type": "ignored", "text": strings.Repeat("x", 16<<10)})
	body += strings.Repeat(padding, 256) + codexTestMessage("assistant", "most recent response")
	if err := os.WriteFile(filepath.Join(root, "rollout-benchmark.jsonl"), []byte(body), 0o600); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sessions, err := Codex(root)
		if err != nil || len(sessions) != 1 {
			b.Fatalf("scan = %d sessions, error %v", len(sessions), err)
		}
	}
}

func TestCodexSkipsArchivedSessions(t *testing.T) {
	root := t.TempDir()
	arch := filepath.Join(root, "archived_sessions")
	if err := os.MkdirAll(arch, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"session_meta","payload":{"id":"old","cwd":"/repo"}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"old archived chat"}]}}
`
	if err := os.WriteFile(filepath.Join(arch, "rollout-old.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Codex(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("archived sessions leaked: %+v", got)
	}
}
