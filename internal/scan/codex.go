package scan

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	markSessionMeta  = []byte(`"session_meta"`)
	markResponseItem = []byte(`"response_item"`)
	markTurnContext  = []byte(`"turn_context"`)
)

// Codex lists Codex CLI rollouts under root (~/.codex/sessions).
// Archived and non-user sessions are skipped. Complete records are streamed
// so large instructions and long turns cannot hide a resumable conversation.
func Codex(root string) ([]Session, error) {
	var paths []string
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi == nil {
			return nil
		}
		if fi.IsDir() && fi.Name() == "archived_sessions" {
			return filepath.SkipDir
		}
		if fi.IsDir() {
			return nil
		}
		name := fi.Name()
		if strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl") {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	out := make([]Session, 0, len(paths))
	for _, p := range paths {
		if s, ok := codexOne(p); ok {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Modified.After(out[j].Modified)
	})
	markMissing(out)
	return out, nil
}

func codexOne(path string) (Session, bool) {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() == 0 {
		return Session{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return Session{}, false
	}
	defer f.Close()
	meta, err := readCodex(f)
	if err != nil || meta.Hidden {
		return Session{}, false
	}
	if meta.ID == "" || meta.Cwd == "" || meta.Title == "" {
		return Session{}, false
	}
	return Session{
		ID:       meta.ID,
		Path:     path,
		Title:    meta.Title,
		AITitled: false,
		Cwd:      meta.Cwd,
		Branch:   meta.Branch,
		ParentID: meta.ParentID,
		Model:    meta.Model,
		Modified: fi.ModTime(),
		Preview:  meta.Preview,
		Tool:     "codex",
	}, true
}

type codexMeta struct {
	ID       string
	Cwd      string
	Title    string
	Model    string
	Branch   string
	ParentID string
	Preview  []Turn
	Hidden   bool
}

type codexRecord struct {
	Type    string `json:"type"`
	Payload struct {
		ID       string          `json:"id"`
		Cwd      string          `json:"cwd"`
		Type     string          `json:"type"`
		Role     string          `json:"role"`
		Model    string          `json:"model"`
		ParentID string          `json:"forked_from_id"`
		Source   json.RawMessage `json:"source"`
		Content  json.RawMessage `json:"content"`
		Git      struct {
			Branch string `json:"branch"`
		} `json:"git"`
	} `json:"payload"`
}

// readCodex owns both record recovery and Codex metadata semantics. The buffer
// grows for a single oversized record, never for the whole transcript.
func readCodex(f io.Reader) (codexMeta, error) {
	reader := bufio.NewReaderSize(f, 64<<10)
	var record []byte
	var parser codexParser
	for {
		fragment, err := reader.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			record = append(record, fragment...)
			continue
		}
		line := fragment
		if len(record) != 0 {
			record = append(record, fragment...)
			line = record
		}
		parser.consume(line)
		record = record[:0]
		if parser.meta.Hidden {
			return parser.meta, nil
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return codexMeta{}, err
		}
	}
	parser.meta.Title = bestTitle(parser.candidates)
	if parser.meta.Model == "" {
		parser.meta.Model = parser.legacyModel
	}
	return parser.meta, nil
}

type codexParser struct {
	meta        codexMeta
	seenMeta    bool
	candidates  []string
	legacyModel string
}

func (p *codexParser) consume(line []byte) {
	if !bytes.Contains(line, markSessionMeta) && !bytes.Contains(line, markResponseItem) && !bytes.Contains(line, markTurnContext) {
		return
	}
	r, ok := decodeCodex(line)
	if !ok {
		return // an active rollout may end with an incomplete record
	}
	switch r.Type {
	case "session_meta":
		if p.seenMeta {
			return // inherited history may contain another session's header
		}
		p.seenMeta = true
		p.meta.ID, p.meta.Cwd = r.Payload.ID, r.Payload.Cwd
		p.meta.Branch, p.meta.ParentID = r.Payload.Git.Branch, r.Payload.ParentID
		p.meta.Hidden = codexNonUserSource(r.Payload.Source)
	case "turn_context":
		if r.Payload.Model != "" {
			p.meta.Model = r.Payload.Model
		}
		if r.Payload.Cwd != "" {
			p.meta.Cwd = r.Payload.Cwd
		}
	case "response_item":
		if r.Payload.Type != "message" || (r.Payload.Role != "user" && r.Payload.Role != "assistant") {
			return
		}
		if r.Payload.Model != "" {
			p.legacyModel = r.Payload.Model
		}
		raw := codexText(r.Payload.Content)
		var text string
		if r.Payload.Role == "user" {
			text = codexPrompt(raw)
			if text != "" && len(p.candidates) < titleCandidates {
				p.candidates = append(p.candidates, text)
			}
		} else {
			text = codexDisplayText(raw)
		}
		if text != "" {
			p.meta.Preview = appendTurn(p.meta.Preview, Turn{Role: r.Payload.Role, Text: text})
		}
	}
}

func codexNonUserSource(raw json.RawMessage) bool {
	var source string
	if json.Unmarshal(raw, &source) == nil {
		return source == "subagent" || strings.HasPrefix(source, "subagent_") || source == "internal" || strings.HasPrefix(source, "internal_")
	}
	var tagged map[string]json.RawMessage
	if json.Unmarshal(raw, &tagged) != nil {
		return false
	}
	_, subagent := tagged["subagent"]
	_, internal := tagged["internal"]
	return subagent || internal
}

func decodeCodex(line []byte) (codexRecord, bool) {
	var r codexRecord
	if json.Unmarshal(line, &r) != nil {
		return r, false
	}
	return r, true
}

func codexText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var b strings.Builder
	for _, blk := range blocks {
		if (blk.Type == "text" || blk.Type == "input_text" || blk.Type == "output_text") && blk.Text != "" {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// Leading context is classified before display markup is stripped. Inline tag
// references and unknown wrappers may contain a real request and stay intact.
var codexContextBlocks = func() []*regexp.Regexp {
	names := []string{
		"environment_context", "user_instructions", "permissions instructions",
		"external_codex_apps_open_page", "external_codex_apps_context",
		"workspace_context", "turn_aborted", "skills_instructions", "skill",
	}
	var patterns []*regexp.Regexp
	for _, name := range names {
		patterns = append(patterns, regexp.MustCompile(`(?s)^<`+name+`(?:\s[^>]*)?>.*?</`+name+`>`))
	}
	return patterns
}()

func codexPrompt(raw string) string {
	text := strings.TrimSpace(raw)
	for {
		header, _, multiline := strings.Cut(text, "\n")
		header = strings.TrimSpace(header)
		if header == "# AGENTS.md instructions" || header == "AGENTS.md instructions" ||
			(multiline && strings.HasPrefix(header, "# AGENTS.md instructions for ")) {
			return ""
		}
		stripped := false
		for _, pattern := range codexContextBlocks {
			if span := pattern.FindStringIndex(text); span != nil {
				text = strings.TrimSpace(text[span[1]:])
				stripped = true
				break
			}
		}
		if !stripped {
			break
		}
	}
	return codexDisplayText(text)
}

func codexDisplayText(text string) string {
	text = loneTag.ReplaceAllString(text, " ")
	text = SanitizeDisplayText(text)
	return truncate(strings.Join(strings.Fields(text), " "), titleMaxLen)
}
