// Package toml implements the tiny TOML subset container-bin reads and writes:
// quoted strings, arrays of quoted strings, booleans, and comment stripping.
// It is deliberately not a general TOML implementation.
package toml

import (
	"errors"
	"strconv"
	"strings"
)

func StripComment(s string) string {
	inQuote := false
	escaped := false
	for i, r := range s {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && inQuote {
			escaped = true
			continue
		}
		if r == '"' {
			inQuote = !inQuote
			continue
		}
		if r == '#' && !inQuote {
			return s[:i]
		}
	}
	return s
}

func ParseQuoted(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", errors.New("expected quoted string")
	}
	v, err := strconv.Unquote(s)
	if err != nil {
		return "", err
	}
	return v, nil
}

func ParseBool(s string) (bool, error) {
	switch strings.TrimSpace(s) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errors.New("expected true or false")
	}
}

func ParseStringArray(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "[]" {
		return nil, nil
	}
	if len(s) < 2 || s[0] != '[' || s[len(s)-1] != ']' {
		return nil, errors.New("expected array of quoted strings")
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	var out []string
	for len(inner) > 0 {
		inner = strings.TrimSpace(inner)
		if !strings.HasPrefix(inner, "\"") {
			return nil, errors.New("array elements must be quoted strings")
		}
		end := -1
		escaped := false
		for i := 1; i < len(inner); i++ {
			if escaped {
				escaped = false
				continue
			}
			if inner[i] == '\\' {
				escaped = true
				continue
			}
			if inner[i] == '"' {
				end = i
				break
			}
		}
		if end < 0 {
			return nil, errors.New("unterminated string")
		}
		v, err := strconv.Unquote(inner[:end+1])
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		inner = strings.TrimSpace(inner[end+1:])
		if inner == "" {
			break
		}
		if inner[0] != ',' {
			return nil, errors.New("expected comma between array elements")
		}
		inner = inner[1:]
	}
	return out, nil
}

func Quote(s string) string { return strconv.Quote(s) }
func Array(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = strconv.Quote(x)
	}
	return "[" + strings.Join(q, ", ") + "]"
}
