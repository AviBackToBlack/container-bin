package toml

import (
	"testing"
)

func TestTomlArray(t *testing.T) {
	got := Array([]string{"a", "b c"})
	if got != `["a", "b c"]` {
		t.Fatalf("tomlArray = %q", got)
	}
}
