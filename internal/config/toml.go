package config

import (
	"fmt"
	"strconv"
	"strings"
)

// value is one parsed config entry: a scalar, or a list of strings.
type value struct {
	scalar string
	list   []string
	isList bool
}

// parseTOML reads the subset of TOML this config needs: top-level
// `key = value` lines, where a value is a number, a quoted string, or an array
// of quoted strings on one line. Sections, tables, and multi-line arrays are
// not accepted — a config that uses them gets an error naming the line rather
// than a silently ignored setting.
//
// Hand-written because the dependency budget is three libraries and none of
// them is a TOML parser; the day this config grows a table is the day to spend
// one of them.
func parseTOML(src string) (map[string]value, error) {
	out := map[string]value{}
	for i, line := range strings.Split(src, "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		if strings.HasPrefix(s, "[") {
			return nil, fmt.Errorf("line %d: sections are not supported in this config", i+1)
		}
		key, rest, ok := strings.Cut(s, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: expected `key = value`, got %q", i+1, s)
		}
		key = strings.TrimSpace(key)
		rest = strings.TrimSpace(stripComment(rest))
		if key == "" || rest == "" {
			return nil, fmt.Errorf("line %d: expected `key = value`, got %q", i+1, s)
		}
		if strings.HasPrefix(rest, "[") {
			list, err := parseList(rest)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", i+1, err)
			}
			out[key] = value{list: list, isList: true}
			continue
		}
		if unquoted, err := strconv.Unquote(rest); err == nil {
			out[key] = value{scalar: unquoted}
			continue
		}
		out[key] = value{scalar: rest}
	}
	return out, nil
}

// stripComment drops a trailing `# ...`, but only outside quotes.
func stripComment(s string) string {
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case '#':
			if !inQuote {
				return s[:i]
			}
		}
	}
	return s
}

func parseList(s string) ([]string, error) {
	if !strings.HasSuffix(s, "]") {
		return nil, fmt.Errorf("an array must open and close on one line: %q", s)
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return []string{}, nil
	}
	parts := splitTopLevel(inner)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		unquoted, err := strconv.Unquote(p)
		if err != nil {
			return nil, fmt.Errorf("array elements must be quoted strings: %q", p)
		}
		out = append(out, unquoted)
	}
	return out, nil
}

func splitTopLevel(s string) []string {
	out := []string{}
	inQuote := false
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case ',':
			if !inQuote {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}
