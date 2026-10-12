package scan

import (
	"bytes"
	"encoding/json"
)

// codexDiscard inspects envelope fields, not marker text inside tool output.
// It also works on an incomplete first fragment. An undecidable fragment is
// retained, so unusual field ordering cannot hide a relevant large record.
func codexDiscard(prefix []byte, seenMeta bool) (kind string, discard bool) {
	kind = codexJSONString(codexJSONField(prefix, "type"))
	switch kind {
	case "":
		return kind, false
	case "session_meta":
		return kind, seenMeta
	case "turn_context":
		return kind, false
	case "response_item":
		payload := codexJSONField(prefix, "payload")
		item := codexJSONString(codexJSONField(payload, "type"))
		if item == "" {
			return kind, false
		}
		if item != "message" {
			return kind, true
		}
		role := codexJSONString(codexJSONField(payload, "role"))
		return kind, role != "" && role != "user" && role != "assistant"
	default:
		return kind, true
	}
}

// These prefix readers only locate fields. json.Unmarshal still validates every
// retained record. Codex envelopes use unique keys; irrelevant records need not
// be validated or buffered merely to throw them away.
func codexJSONField(object []byte, key string) []byte {
	object = bytes.TrimSpace(object)
	if len(object) == 0 || object[0] != '{' {
		return nil
	}
	object = bytes.TrimSpace(object[1:])
	for len(object) > 0 && object[0] == '"' {
		end := codexJSONEnd(object)
		if end == 0 {
			return nil
		}
		name := object[:end]
		object = bytes.TrimSpace(object[end:])
		if len(object) == 0 || object[0] != ':' {
			return nil
		}
		object = bytes.TrimSpace(object[1:])
		if codexJSONString(name) == key {
			return object
		}
		end = codexJSONEnd(object)
		if end == 0 {
			return nil
		}
		object = bytes.TrimSpace(object[end:])
		if len(object) == 0 || object[0] != ',' {
			return nil
		}
		object = bytes.TrimSpace(object[1:])
	}
	return nil
}

func codexJSONString(value []byte) string {
	if len(value) == 0 || value[0] != '"' {
		return ""
	}
	end := codexJSONEnd(value)
	if end == 0 {
		return ""
	}
	value = value[:end]
	if !bytes.ContainsRune(value, '\\') {
		return string(value[1 : len(value)-1])
	}
	var text string
	if json.Unmarshal(value, &text) != nil {
		return ""
	}
	return text
}

// codexJSONEnd returns zero for a value cut off by the reader's buffer. Strings
// are skipped with their escapes intact, including quotes and braces in output.
func codexJSONEnd(value []byte) int {
	if len(value) == 0 {
		return 0
	}
	if value[0] == '"' {
		for offset := 1; offset < len(value); {
			quote := bytes.IndexByte(value[offset:], '"')
			if quote < 0 {
				return 0
			}
			quote += offset
			var slashes int
			for i := quote - 1; i >= 0 && value[i] == '\\'; i-- {
				slashes++
			}
			if slashes%2 == 0 {
				return quote + 1
			}
			offset = quote + 1
		}
		return 0
	}
	if value[0] == '{' || value[0] == '[' {
		depth := 0
		for i := 0; i < len(value); i++ {
			switch value[i] {
			case '"':
				end := codexJSONEnd(value[i:])
				if end == 0 {
					return 0
				}
				i += end - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1
				}
			}
		}
		return 0
	}
	for i, c := range value {
		if c == ',' || c == '}' || c == ']' || c == ' ' || c == '\n' || c == '\r' || c == '\t' {
			return i
		}
	}
	return 0
}
