package scan

import (
	"encoding/json"
	"regexp"
	"strings"
)

type codexContextBlock struct {
	open  *regexp.Regexp
	close string
}

// Classify context before stripping display markup. Wrappers at message/line
// boundaries are structural; inline examples remain searchable human text.
var codexContextBlocks = func() []codexContextBlock {
	names := []string{
		"environment_context", "user_instructions", "permissions instructions",
		"external_codex_apps_open_page", "external_codex_apps_context",
		"workspace_context", "turn_aborted", "skills_instructions", "skill",
		"recommended_plugins", "user_shell_command", "task-notification", "subagent_notification",
	}
	var blocks []codexContextBlock
	for _, name := range names {
		blocks = append(blocks, codexContextBlock{
			regexp.MustCompile(`<` + name + `(?:\s[^>]*)?>`), "</" + name + ">",
		})
	}
	return blocks
}()

func codexPrompt(raw string) string {
	text := strings.TrimSpace(raw)
	if codexNotification(text) || strings.HasPrefix(text, "Here is a list of plugins that are available but not installed") {
		return ""
	}
	text = codexWithoutAgents(text)
	for offset := 0; offset < len(text); {
		// Source order matters: removing one leading wrapper can make the next
		// adjacent wrapper structural, irrespective of the configured name order.
		start, end := len(text), len(text)
		var block codexContextBlock
		for _, candidate := range codexContextBlocks {
			if span := candidate.open.FindStringIndex(text[offset:]); span != nil && offset+span[0] < start {
				start, end, block = offset+span[0], offset+span[1], candidate
			}
		}
		if start == len(text) {
			break
		}
		body := text[end:]
		closeEnd := codexContextEnd(body, block)
		lineStart := strings.LastIndexByte(text[:start], '\n') + 1
		structural := strings.TrimSpace(text[lineStart:start]) == ""
		machine := codexContextBody(body)
		if closeEnd >= 0 && (structural || machine) {
			text = text[:start] + " " + body[closeEnd:]
			offset = start
		} else if closeEnd < 0 && structural && (machine || strings.HasPrefix(body, "\n") || strings.HasPrefix(body, "\r\n")) {
			text = text[:start]
			break
		} else if closeEnd >= 0 {
			offset = end + closeEnd // preserve the complete inline example
		} else {
			offset = end
		}
	}
	text = codexWithoutAgents(strings.TrimSpace(text))
	if codexNotification(text) || strings.HasPrefix(text, "Here is a list of plugins that are available but not installed") {
		return ""
	}
	return codexDisplayText(text)
}

func codexContextEnd(body string, block codexContextBlock) int {
	depth, offset := 1, 0
	for depth > 0 {
		close := strings.Index(body[offset:], block.close)
		if close < 0 {
			return -1
		}
		open := block.open.FindStringIndex(body[offset:])
		if open != nil && open[0] < close {
			depth++
			offset += open[1]
		} else {
			depth--
			offset += close + len(block.close)
		}
	}
	return offset
}

func codexWithoutAgents(text string) string {
	header, body, multiline := strings.Cut(text, "\n")
	header = strings.TrimSpace(header)
	if header != "# AGENTS.md instructions" && header != "AGENTS.md instructions" &&
		!(multiline && strings.HasPrefix(header, "# AGENTS.md instructions for ")) {
		return text
	}
	body = strings.TrimSpace(body)
	if strings.HasPrefix(body, "<INSTRUCTIONS>") {
		if end := strings.Index(body, "</INSTRUCTIONS>"); end >= 0 {
			return strings.TrimSpace(body[end+len("</INSTRUCTIONS>"):])
		}
		return ""
	}
	// A blank paragraph may be another instruction. Only an explicit request
	// separator establishes where an unwrapped manual ends.
	for _, separator := range []string{"\n## User request\n", "\n<user_request>"} {
		if _, request, ok := strings.Cut(body, separator); ok {
			return strings.TrimSpace(strings.TrimSuffix(request, "</user_request>"))
		}
	}
	return ""
}

func codexContextBody(body string) bool {
	body = strings.TrimSpace(body)
	for _, prefix := range []string{
		"Current directory:", "Current working directory:", "<cwd>", "<shell>",
		"<current_date>", "<permissions", "Exit code:", "<command>",
		"Here is a list of plugins that are available but not installed",
	} {
		if strings.HasPrefix(body, prefix) {
			return true
		}
	}
	return false
}

func codexNotification(text string) bool {
	if !strings.HasPrefix(text, "{") {
		return false
	}
	var notice struct {
		AgentPath string                     `json:"agent_path"`
		Status    map[string]json.RawMessage `json:"status"`
	}
	return json.Unmarshal([]byte(text), &notice) == nil && notice.AgentPath != "" && len(notice.Status) != 0
}

func codexTitleGeneration(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "Generate metadata for a coding agent based on the user prompt.") && strings.Contains(text, "Title:")
}
