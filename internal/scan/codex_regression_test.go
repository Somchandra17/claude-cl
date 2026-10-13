package scan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexInjectedUserTurns(t *testing.T) {
	request := "fix the actual session search bug"
	cases := []struct {
		name    string
		prompts []string
		want    string
	}{
		{"short acknowledgement", []string{"hi", request}, request},
		{"status before request", []string{`{"agent_path":"019d-test","status":{"completed":"I have completed the requested implementation and checked the new behavior across all relevant session workflows."}}`, request}, request},
		{"wrapped status before request", []string{"<subagent_notification>\n{\"agent_path\":\"019d-test\",\"status\":{\"completed\":\"I have completed the requested implementation and checked the new behavior across all relevant session workflows.\"}}\n</subagent_notification>", request}, request},
		{"plugin boilerplate", []string{"Here is a list of plugins that are available but not installed.\n- GitHub\n- Gmail", request}, request},
		{"trailing environment", []string{request + "\n<environment_context>Current directory: /repo; shell: zsh</environment_context>"}, request},
		{"leading agents and request", []string{"# AGENTS.md instructions for /repo\nFollow repository instructions.\n\n## User request\n" + request}, request},
		{"shell output", []string{"<user_shell_command>ls -la\nExit code: 0\nOutput: files</user_shell_command>", request}, request},
		{"unclosed injected wrapper", []string{"<environment_context>Current directory: /repo; shell: zsh", request}, request},
		{"same line trailing environment", []string{request + " <environment_context>Current directory: /repo</environment_context>"}, request},
		{"wrapped agents and request", []string{"# AGENTS.md instructions for /repo\n\n<INSTRUCTIONS>Follow repository instructions.\n\nAdditional instructions.</INSTRUCTIONS>\n" + request}, request},
		{"multiple injected notices", []string{"<recommended_plugins>Here is a list of plugins that are available but not installed.</recommended_plugins>", `<task-notification>machine status</task-notification>`, request}, request},
		{"adjacent wrappers in reverse configured order", []string{"<environment_context>Current directory: /repo</environment_context><skill>manual content</skill><user_instructions>other manual content</user_instructions>\n" + request}, request},
		{"nested environment", []string{"<environment_context>Current directory: /repo\n<environment_context>Nested injected environment</environment_context>\nAdditional machine context should never describe a session</environment_context>\n" + request}, request},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := codexTestMeta(nil)
			for _, prompt := range tc.prompts {
				body += codexTestMessage("user", prompt)
			}
			sessions := codexTestScan(t, body)
			if len(sessions) != 1 {
				t.Fatalf("sessions=%d, want 1", len(sessions))
			}
			if sessions[0].Title != tc.want {
				t.Errorf("title=%q, want %q", sessions[0].Title, tc.want)
			}
		})
	}
}

func TestCodexRejectsCorruptForkHeader(t *testing.T) {
	for _, header := range []string{
		`{"type":"session_meta","payload":{"id":"fork","cwd":`,
		`{"payload":{"id":"fork","cwd":"/repo"} "type":"session_meta"}`,
		`not json`,
		`{"type":"event_msg","payload":{"type":"ignored"}}`,
		`{ "type" : "session_meta", "payload" : { "id" : 42, "cwd" : "/repo" } }`,
		`{"type":"session_meta","payload":{"id":"fork","cwd":"/repo","source":` + strings.Repeat(" ", 96<<10),
	} {
		body := header + "\n" + codexTestMeta(map[string]any{"id": "parent"})
		body += codexTestMessage("user", "fix this forked conversation please")
		if sessions := codexTestScan(t, body); len(sessions) != 0 {
			t.Errorf("listed corrupt fork using id=%q, want omitted", sessions[0].ID)
		}
	}
}

func TestCodexRepeatedAgentsInstructionsDoNotFillTitleCandidates(t *testing.T) {
	instructions := "# AGENTS.md instructions for /repo\nFollow these repository conventions.\n\nUse readable names and keep all changes within the existing project structure."
	body := codexTestMeta(nil)
	for range 6 {
		body += codexTestMessage("user", instructions)
	}
	body += codexTestMessage("user", "fix the real request after repository instructions")
	sessions := codexTestScan(t, body)
	if len(sessions) != 1 || sessions[0].Title != "fix the real request after repository instructions" || len(sessions[0].Preview) != 1 {
		t.Fatalf("repository instructions became a prompt: %+v", sessions)
	}
}

func TestCodexSkipsInternalTitleGenerator(t *testing.T) {
	body := codexTestMeta(map[string]any{"source": "exec", "originator": "paseo"})
	body += codexTestMessage("user", "Generate metadata for a coding agent based on the user prompt. Title: short description of the task")
	if sessions := codexTestScan(t, body); len(sessions) != 0 {
		t.Fatalf("listed internal title-generator call: title=%q", sessions[0].Title)
	}
	// Originator alone is not grounds to omit genuine integration sessions.
	body = codexTestMeta(map[string]any{"source": "exec", "originator": "paseo"}) + codexTestMessage("user", "fix the genuine integration session")
	if sessions := codexTestScan(t, body); len(sessions) != 1 {
		t.Fatal("omitted a genuine Paseo session")
	}
}

func TestCodexLargeRecordsAndFieldOrder(t *testing.T) {
	padding, _ := json.Marshal(strings.Repeat(`escaped \" response_item message user { } `, 1<<16))
	message := `{"content":[{"type":"input_text","text":"find this unusually ordered session"}],"role":"user","type":"message"}`
	for _, record := range []string{
		`{"type":"response_item","payload":{"type":"function_call_output","output":` + string(padding) + `}}`,
		`{"payload":{"output":` + string(padding) + `,"type":"function_call_output"},"type":"response_item"}`,
		`{"type":"response_item","payload":{"padding":` + string(padding) + `,"type":"message","role":"assistant","model":"legacy-model","content":"recent assistant reply"}}`,
	} {
		body := codexTestMeta(nil) + record + "\n"
		body += `{"payload":` + message + `,"type":"response_item"}` + "\n"
		body += codexTestRecord("turn_context", map[string]string{"cwd": "/latest", "model": "latest-model"})
		sessions := codexTestScan(t, body)
		if len(sessions) != 1 || sessions[0].Title != "find this unusually ordered session" || sessions[0].Cwd != "/latest" || sessions[0].Model != "latest-model" {
			t.Fatalf("lost metadata after a large record: %+v", sessions)
		}
	}
}

func TestCodexListsAllFilesNewestFirst(t *testing.T) {
	root := t.TempDir()
	for i := range 40 {
		path := filepath.Join(root, fmt.Sprintf("rollout-%02d.jsonl", i))
		body := codexTestMeta(map[string]any{"id": fmt.Sprint(i)}) + codexTestMessage("user", "fix this independently scanned session")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Unix(int64(i+1), 0)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	sessions, err := Codex(root)
	if err != nil || len(sessions) != 40 {
		t.Fatalf("listed %d sessions, error=%v", len(sessions), err)
	}
	for i, s := range sessions {
		if s.ID != fmt.Sprint(39-i) {
			t.Errorf("session %d id=%q, want %d", i, s.ID, 39-i)
		}
	}
}

func BenchmarkCodexRecordMix(b *testing.B) {
	for _, kind := range []string{"event_msg", "response_item"} {
		b.Run(kind, func(b *testing.B) {
			body := codexTestMeta(nil) + codexTestMessage("user", "fix the record parser performance")
			padding := codexTestRecord(kind, map[string]any{"type": "function_call_output", "call_id": "tool-call", "output": strings.Repeat("x", 16<<10)})
			body += strings.Repeat(padding, 256)
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				meta, err := readCodex(strings.NewReader(body))
				if err != nil || meta.ID != "session" {
					b.Fatalf("id=%q error=%v", meta.ID, err)
				}
			}
		})
	}
}
