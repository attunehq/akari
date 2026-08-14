package parser

import (
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// parseTime parses the timestamp formats these agents emit (RFC3339, with or
// without sub-second precision). A blank or unparseable value yields the zero
// time.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// toolFilePath projects the file path out of a parsed tool input, reading the
// same keys in the same order as the sentinel writers (filePathKeys), so a raw
// line and its lifted rewrite always yield the same FilePath.
func toolFilePath(input gjson.Result) string {
	for _, key := range filePathKeys {
		if v := input.Get(key); v.Type == gjson.String {
			return v.String()
		}
	}
	return ""
}

// toolCategory maps a raw tool name to a coarse category used for filtering and
// stats. Unknown tools get an empty category rather than a guess.
func toolCategory(name string) string {
	switch strings.ToLower(name) {
	case "read", "readfile", "read_file", "view", "cat":
		return "read"
	case "write", "writefile", "create":
		return "write"
	case "edit", "apply_patch", "applypatch", "str_replace", "strreplace", "search_replace", "multiedit":
		return "edit"
	case "bash", "shell", "shell_command", "exec_command", "run", "execute", "run_terminal_command":
		return "bash"
	case "glob", "grep", "search", "find", "rg", "codebase_search":
		return "search"
	default:
		return ""
	}
}
