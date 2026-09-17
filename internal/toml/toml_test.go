package toml

import (
	"strings"
	"testing"
)

func TestTomlArray(t *testing.T) {
	got := Array([]string{"a", "b c"})
	if got != `["a", "b c"]` {
		t.Fatalf("tomlArray = %q", got)
	}
}

func TestParseSectionHeader(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		section    string
		isHeader   bool
		wantErrSub string
	}{
		{name: "basic", line: "[tools.node]", section: "tools.node", isHeader: true},
		{name: "whitespace_crlf", line: " \t[defaults.node] \r\n", section: "defaults.node", isHeader: true},
		{name: "inline_comment", line: "[tools.node] # note", section: "tools.node", isHeader: true},
		{name: "quoted_hash_value", line: `value = "literal # value" # note`},
		{name: "comment", line: "# [tools.node]"},
		{name: "blank", line: "  \t"},
		{name: "array_table", line: "[[tools.node]]", wantErrSub: "array table"},
		{name: "missing_close", line: "[tools.node", wantErrSub: "malformed"},
		{name: "missing_open", line: "tools.node]"},
		{name: "empty", line: "[]", wantErrSub: "malformed"},
		{name: "empty_whitespace", line: "[ ]", wantErrSub: "malformed"},
		{name: "extra_close", line: "[tools.node]]", wantErrSub: "malformed"},
		{name: "two_headers", line: "[tools.node][defaults.node]", wantErrSub: "malformed"},
		{name: "trailing_content", line: "[tools.node] trailing", wantErrSub: "malformed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			section, isHeader, err := ParseSectionHeader(tc.line)
			if tc.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Fatalf("ParseSectionHeader error = %v, want substring %q", err, tc.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if section != tc.section || isHeader != tc.isHeader {
				t.Fatalf("ParseSectionHeader = (%q, %v), want (%q, %v)", section, isHeader, tc.section, tc.isHeader)
			}
		})
	}
}
