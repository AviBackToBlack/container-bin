package policy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadAtMissingIsUnmanaged(t *testing.T) {
	p, err := loadAt(filepath.Join(t.TempDir(), "missing.toml"), func(string) error { return nil }, time.Now())
	if err != nil || p.Managed() {
		t.Fatalf("loadAt missing = (%+v, %v), want unmanaged nil", p, err)
	}
}

func TestLoadAtStrictAndCanonical(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.toml")
	contents := "policy_version = 1\nrequire_lock = true\nallow_local_images = true\nallowed_repositories = [\"registry-1.docker.io/library\", \"GHCR.IO/Astral-SH\", \"ghcr.io/astral-sh\"]\nexpires_at = \"2030-01-02T03:04:05Z\"\n"
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := loadAt(path, func(got string) error {
		if got != path {
			t.Fatalf("ownership path = %q, want %q", got, path)
		}
		return nil
	}, time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Managed() || !p.RequireLock || !p.AllowLocalImages || p.Fingerprint == "" {
		t.Fatalf("unexpected parsed policy: %+v", p)
	}
	want := []string{"docker.io/library", "ghcr.io/astral-sh"}
	if strings.Join(p.AllowedRepositories, ",") != strings.Join(want, ",") {
		t.Fatalf("allowed repositories = %v, want %v", p.AllowedRepositories, want)
	}
	if !strings.Contains(p.Summary(), "fingerprint=sha256:") || !strings.Contains(p.Summary(), path) {
		t.Fatalf("summary missing provenance: %q", p.Summary())
	}
}

func TestLoadAtOwnershipFailureIsCoded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.WriteFile(path, []byte("policy_version = 1\nrequire_lock = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := loadAt(path, func(string) error { return errors.New("writable") }, time.Now())
	assertPolicyCode(t, err, "ownership")
}

func TestParseRejectsInvalidPolicies(t *testing.T) {
	now := time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name, contents, code string
	}{
		{"missing version", "require_lock = true\n", "version"},
		{"unknown version", "policy_version = 2\nrequire_lock = true\n", "version"},
		{"duplicate", "policy_version = 1\nrequire_lock = true\nrequire_lock = false\n", "syntax"},
		{"unknown key", "policy_version = 1\nrequire_lock = true\nsurprise = true\n", "syntax"},
		{"section", "policy_version = 1\nrequire_lock = true\n[extra]\n", "syntax"},
		{"no controls", "policy_version = 1\nallow_local_images = true\n", "syntax"},
		{"expired", "policy_version = 1\nrequire_lock = true\nexpires_at = \"2028-01-01T00:00:00Z\"\n", "expired"},
		{"bad rule", "policy_version = 1\nallowed_repositories = [\"ghcr.io//team\"]\n", "syntax"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse("policy.toml", []byte(tc.contents), now)
			assertPolicyCode(t, err, tc.code)
		})
	}
}

func TestCanonicalRepository(t *testing.T) {
	cases := map[string]string{
		"python:3.13":                                       "docker.io/library/python",
		"docker.io/python@sha256:aaaa":                      "docker.io/library/python",
		"index.docker.io/library/python:latest":             "docker.io/library/python",
		"registry-1.docker.io/team/tool@sha256:bbbb":        "docker.io/team/tool",
		"astral-sh/uv:0.8":                                  "docker.io/astral-sh/uv",
		"GHCR.IO/AviBackToBlack/Container-Bin:v1":           "ghcr.io/avibacktoblack/container-bin",
		"localhost:5000/team/tool:dev":                      "localhost:5000/team/tool",
		"example.com:5443/nested/team/tool@sha256:deadbeef": "example.com:5443/nested/team/tool",
	}
	for input, want := range cases {
		got, err := CanonicalRepository(input)
		if err != nil || got != want {
			t.Errorf("CanonicalRepository(%q) = (%q, %v), want %q", input, got, err, want)
		}
	}
	for _, bad := range []string{"", "https://ghcr.io/team/tool", `ghcr.io\team\tool`, "ghcr.io//team", "../tool", "ghcr.io/team/../tool", "sha256:" + strings.Repeat("a", 64), "python@", "ghcr.io/team/tool:bad:tag", "localhost:abc/team/tool"} {
		if got, err := CanonicalRepository(bad); err == nil {
			t.Errorf("CanonicalRepository(%q) = %q, want error", bad, got)
		}
	}
}

func TestAuthorizeImage(t *testing.T) {
	p := Policy{SchemaVersion: 1, RequireLock: true, AllowedRepositories: []string{"docker.io/library", "ghcr.io/team"}}
	if err := p.AuthorizeImage("python:3.13", true, false); err != nil {
		t.Fatal(err)
	}
	if err := p.AuthorizeImage("ghcr.io/team/sub/tool:1", true, false); err != nil {
		t.Fatal(err)
	}
	assertPolicyCode(t, p.AuthorizeImage("python:3.13", false, false), "lock_required")
	assertPolicyCode(t, p.AuthorizeImage("ghcr.io/teamster/tool:1", true, false), "repository_denied")
	assertPolicyCode(t, p.AuthorizeImage("sha256:"+strings.Repeat("a", 64), true, true), "local_image_denied")

	p.AllowLocalImages = true
	if err := p.AuthorizeImage("local-tool:dev", true, true); err != nil {
		t.Fatalf("explicit local exception rejected: %v", err)
	}
	if err := (Policy{}).AuthorizeImage("anything", false, true); err != nil {
		t.Fatalf("unmanaged policy changed behavior: %v", err)
	}
	p = Policy{SchemaVersion: 1, AllowedRepositories: []string{"ghcr.io/team"}}
	if err := p.AuthorizeResolvedImage("ghcr.io/team/tool:1", "evil.example/tool@sha256:"+strings.Repeat("a", 64), false); err == nil {
		t.Fatal("resolved lock repository bypassed allowlist")
	}
}

func assertPolicyCode(t *testing.T, err error, want string) {
	t.Helper()
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != want {
		t.Fatalf("error = %v, want policy code %q", err, want)
	}
}
