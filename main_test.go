package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/registry"
)

func TestLockFileRoundTrip(t *testing.T) {
	lf := &LockFile{Version: 1, Images: map[string]LockEntry{
		"node:24-slim": {
			Configured: "node:24-slim",
			Resolved:   "node@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		"ghcr.io/jqlang/jq:latest": {
			Configured: "ghcr.io/jqlang/jq:latest",
			Resolved:   "ghcr.io/jqlang/jq@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Digest:     "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
	}}
	dir := t.TempDir()
	path := filepath.Join(dir, "container-bin.lock")
	if err := writeLockFile(path, lf); err != nil {
		t.Fatal(err)
	}
	got, err := loadLockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || len(got.Images) != 2 {
		t.Fatalf("unexpected lock: %#v", got)
	}
	if got.Images["node:24-slim"].Resolved != lf.Images["node:24-slim"].Resolved {
		t.Fatalf("node resolved mismatch: %#v", got.Images["node:24-slim"])
	}
}

func TestLockFileRejectsWrongEntryID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "container-bin.lock")
	s := `lock_version = 1

[images.deadbeef]
configured = "node:24-slim"
resolved = "node@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
`
	if err := os.WriteFile(path, []byte(s), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadLockFile(path); err == nil {
		t.Fatal("expected wrong entry id rejection")
	}
}

func TestConfiguredImagesDeduplicatesSharedImage(t *testing.T) {
	reg := registry.Registry{Tools: map[string]registry.Tool{
		"node": {Image: "node:24-slim"},
		"npm":  {Image: "node:24-slim"},
		"jq":   {Image: "ghcr.io/jqlang/jq:latest"},
	}}
	got := configuredImages(reg)
	want := []string{"ghcr.io/jqlang/jq:latest", "node:24-slim"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestImageRepository(t *testing.T) {
	cases := map[string]string{
		"node:24-slim":                         "node",
		"ghcr.io/jqlang/jq:latest":             "ghcr.io/jqlang/jq",
		"registry.example:5000/a/b:tag":        "registry.example:5000/a/b",
		"registry.example:5000/a/b@sha256:abc": "registry.example:5000/a/b",
	}
	for in, want := range cases {
		if got := imageRepository(in); got != want {
			t.Fatalf("imageRepository(%q)=%q want %q", in, got, want)
		}
	}
}

func TestVersionDefaultIsDev(t *testing.T) {
	// Release builds override this via -ldflags "-X main.version=vX.Y.Z".
	if version != "dev" {
		t.Fatalf("default version = %q, want \"dev\"", version)
	}
}

func TestCanonicalRepository(t *testing.T) {
	cases := map[string]string{
		"python":                       "python",
		"library/python":               "python",
		"docker.io/library/python":     "python",
		"index.docker.io/library/node": "node",
		"docker.io/mikefarah/yq":       "mikefarah/yq",
		"ghcr.io/jqlang/jq":            "ghcr.io/jqlang/jq",
		"registry.example:5000/a/b":    "registry.example:5000/a/b",
		"lscr.io/linuxserver/ffmpeg":   "lscr.io/linuxserver/ffmpeg",
	}
	for in, want := range cases {
		if got := canonicalRepository(in); got != want {
			t.Fatalf("canonicalRepository(%q)=%q want %q", in, got, want)
		}
	}
}

func TestMatchRepoDigestNormalizesDockerHubRefs(t *testing.T) {
	digests := []string{"python@sha256:" + strings.Repeat("a", 64)}
	for _, configured := range []string{"python:3.13-slim", "library/python:3.13-slim", "docker.io/library/python:3.13-slim"} {
		got, ok := matchRepoDigest(configured, digests)
		if !ok || got != digests[0] {
			t.Fatalf("matchRepoDigest(%q) = %q, %v; want %q, true", configured, got, ok, digests[0])
		}
	}
}

func TestMatchRepoDigestFailsClosedOnForeignRepo(t *testing.T) {
	digests := []string{"someone/else@sha256:" + strings.Repeat("b", 64)}
	if got, ok := matchRepoDigest("python:3.13-slim", digests); ok {
		t.Fatalf("expected no match for foreign repo digest, got %q", got)
	}
	if _, ok := matchRepoDigest("python:3.13-slim", nil); ok {
		t.Fatal("expected no match for empty digest list")
	}
	if _, ok := matchRepoDigest("python:3.13-slim", []string{"python@md5:oops", "garbage"}); ok {
		t.Fatal("expected malformed digests to be ignored")
	}
}

func TestInvokedNameIsCaseInsensitive(t *testing.T) {
	// Bare names only: filepath.Base separator handling is OS-specific and
	// dispatch only depends on the final path element anyway.
	cases := map[string]string{
		"python.exe":    "python",
		"PYTHON.EXE":    "python",
		"Cb.ExE":        "cb",
		"cb":            "cb",
		"terraform.exe": "terraform",
		"cb-v1.0.0.exe": "cb-v1.0.0",
	}
	for in, want := range cases {
		if got := invokedName(in); got != want {
			t.Fatalf("invokedName(%q)=%q want %q", in, got, want)
		}
	}
}
