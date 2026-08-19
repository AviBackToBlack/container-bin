package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/registry"
)

// These tests cover Expose guard paths that need no Docker daemon.
// The Docker-dependent discovery path (discoverNPMGlobalBins onward) remains
// untested here because it requires a real Docker daemon and a populated
// npm-global volume.

func TestExposeRequiresSourceTool(t *testing.T) {
	reg := registry.Default()
	if err := Expose(reg, filepath.Join(t.TempDir(), "container-bin.toml"), nil); err == nil {
		t.Fatal("expected usage error for empty args")
	} else if !strings.Contains(err.Error(), "usage: cb expose TOOL") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestExposeRejectsUnknownSource(t *testing.T) {
	reg := registry.Default()
	if err := Expose(reg, filepath.Join(t.TempDir(), "container-bin.toml"), []string{"notarealtool"}); err == nil {
		t.Fatal("expected not-found error")
	} else if !strings.Contains(err.Error(), `tool "notarealtool" not found`) {
		t.Fatalf("unexpected error message: %v", err)
	}
}
