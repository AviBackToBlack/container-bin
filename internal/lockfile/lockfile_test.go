package lockfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/policy"
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
		"local/tool:dev": {
			Configured: "local/tool:dev",
			Resolved:   "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			Digest:     "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		},
	}}
	dir := t.TempDir()
	path := filepath.Join(dir, "container-bin.lock")
	if err := Write(path, lf); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || len(got.Images) != 3 {
		t.Fatalf("unexpected lock: %#v", got)
	}
	if got.Images["node:24-slim"].Resolved != lf.Images["node:24-slim"].Resolved {
		t.Fatalf("node resolved mismatch: %#v", got.Images["node:24-slim"])
	}
	if got.Images["local/tool:dev"].Resolved != lf.Images["local/tool:dev"].Resolved {
		t.Fatalf("local resolved mismatch: %#v", got.Images["local/tool:dev"])
	}
}

func validTrustEvidence() *ImageTrustEvidence {
	return &ImageTrustEvidence{
		Version:           1,
		Mechanism:         policy.ImageTrustKeyless,
		Repository:        "docker.io/library/node",
		Digest:            "sha256:" + strings.Repeat("a", 64),
		Signer:            "https://github.com/acme/tools/.github/workflows/release.yml@refs/tags/v1.2.3",
		Issuer:            "https://token.actions.githubusercontent.com",
		BundleSHA256:      strings.Repeat("b", 64),
		VerifiedAt:        "2026-09-26T12:34:56Z",
		Verifier:          "cosign",
		VerifierSHA256:    strings.Repeat("c", 64),
		PolicyFingerprint: strings.Repeat("d", 64),
	}
}

func validTrustLock(version int) *LockFile {
	return &LockFile{Version: version, Images: map[string]LockEntry{
		"node:24-slim": {
			Configured: "node:24-slim",
			Resolved:   "node@sha256:" + strings.Repeat("a", 64),
			Digest:     "sha256:" + strings.Repeat("a", 64),
			Trust:      validTrustEvidence(),
		},
	}}
}

func TestLockFileV2TrustEvidenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "container-bin.lock")
	if err := Write(path, validTrustLock(2)); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := got.Images["node:24-slim"]
	if got.Version != 2 || entry.Trust == nil {
		t.Fatalf("unexpected v2 lock: %#v", got)
	}
	if *entry.Trust != *validTrustEvidence() {
		t.Fatalf("trust evidence mismatch:\n got %#v\nwant %#v", *entry.Trust, *validTrustEvidence())
	}
}

func TestLockFileV2KeyEvidenceRoundTrip(t *testing.T) {
	lf := validTrustLock(2)
	entry := lf.Images["node:24-slim"]
	entry.Trust.Mechanism = policy.ImageTrustKey
	entry.Trust.Signer = strings.Repeat("e", 64)
	entry.Trust.Issuer = ""
	lf.Images[entry.Configured] = entry

	path := filepath.Join(t.TempDir(), "container-bin.lock")
	if err := Write(path, lf); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	trust := got.Images[entry.Configured].Trust
	if trust == nil || trust.Mechanism != policy.ImageTrustKey || trust.Signer != strings.Repeat("e", 64) || trust.Issuer != "" {
		t.Fatalf("unexpected key evidence: %#v", trust)
	}
}

func TestLockFileV2AllowsDigestOnlyEntries(t *testing.T) {
	lf := validTrustLock(2)
	entry := lf.Images["node:24-slim"]
	entry.Trust = nil
	lf.Images[entry.Configured] = entry
	if err := Write(filepath.Join(t.TempDir(), "container-bin.lock"), lf); err != nil {
		t.Fatalf("digest-only v2 lock rejected: %v", err)
	}
}

func TestLockFileV1RejectsTrustEvidence(t *testing.T) {
	err := Write(filepath.Join(t.TempDir(), "container-bin.lock"), validTrustLock(1))
	if err == nil || !strings.Contains(err.Error(), "requires lock_version 2") {
		t.Fatalf("Write error = %v, want lock_version 2 requirement", err)
	}
}

func TestOlderParserRejectsV2Lock(t *testing.T) {
	data := render(validTrustLock(2))
	if _, err := parseLockFile(data, 1); err == nil || !strings.Contains(err.Error(), "supported: 1-1") {
		t.Fatalf("old parser error = %v, want unsupported v2", err)
	}
	if _, err := parseLockFile(render(&LockFile{Version: 1, Images: map[string]LockEntry{}}), 1); err != nil {
		t.Fatalf("old parser rejected v1: %v", err)
	}
}

func TestLockFileRejectsMalformedTrustEvidence(t *testing.T) {
	cases := []struct {
		name string
		edit func(*ImageTrustEvidence, *LockEntry)
		want string
	}{
		{"version", func(e *ImageTrustEvidence, _ *LockEntry) { e.Version = 2 }, "unsupported evidence_version"},
		{"repository", func(e *ImageTrustEvidence, _ *LockEntry) { e.Repository = "ghcr.io/acme/node" }, "does not match canonical"},
		{"digest", func(e *ImageTrustEvidence, _ *LockEntry) { e.Digest = "sha256:" + strings.Repeat("f", 64) }, "does not match the locked digest"},
		{"empty signer", func(e *ImageTrustEvidence, _ *LockEntry) { e.Signer = "" }, "signer must be"},
		{"keyless issuer", func(e *ImageTrustEvidence, _ *LockEntry) { e.Issuer = "http://issuer.example" }, "issuer must be an HTTPS URL"},
		{"key issuer", func(e *ImageTrustEvidence, _ *LockEntry) {
			e.Mechanism, e.Signer, e.Issuer = policy.ImageTrustKey, strings.Repeat("e", 64), "https://issuer.example"
		}, "issuer must be empty"},
		{"key signer", func(e *ImageTrustEvidence, _ *LockEntry) {
			e.Mechanism, e.Signer, e.Issuer = policy.ImageTrustKey, "key.pem", ""
		}, "key signer must be"},
		{"mechanism", func(e *ImageTrustEvidence, _ *LockEntry) { e.Mechanism = "notation" }, "unsupported mechanism"},
		{"bundle", func(e *ImageTrustEvidence, _ *LockEntry) { e.BundleSHA256 = strings.Repeat("B", 64) }, "bundle SHA-256"},
		{"time offset", func(e *ImageTrustEvidence, _ *LockEntry) { e.VerifiedAt = "2026-09-26T13:34:56+01:00" }, "canonical UTC"},
		{"time fraction", func(e *ImageTrustEvidence, _ *LockEntry) { e.VerifiedAt = "2026-09-26T12:34:56.123Z" }, "canonical UTC"},
		{"verifier", func(e *ImageTrustEvidence, _ *LockEntry) { e.Verifier = "cosign.exe" }, "unsupported verifier"},
		{"verifier hash", func(e *ImageTrustEvidence, _ *LockEntry) { e.VerifierSHA256 = "short" }, "verifier SHA-256"},
		{"policy fingerprint", func(e *ImageTrustEvidence, _ *LockEntry) { e.PolicyFingerprint = "short" }, "policy fingerprint"},
		{"local image", func(_ *ImageTrustEvidence, entry *LockEntry) {
			entry.Resolved, entry.Digest = "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("a", 64)
		}, "local image ID cannot carry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lf := validTrustLock(2)
			entry := lf.Images["node:24-slim"]
			tc.edit(entry.Trust, &entry)
			lf.Images[entry.Configured] = entry
			err := Write(filepath.Join(t.TempDir(), "container-bin.lock"), lf)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Write error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLockFileRejectsDuplicateKeysAndSections(t *testing.T) {
	configured := "node:24-slim"
	id := entryID(configured)
	entry := fmt.Sprintf("[images.%s]\nconfigured = %q\nresolved = %q\ndigest = %q\n", id, configured, "node@sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("a", 64))
	cases := []struct {
		name string
		body string
		want string
	}{
		{"top level", "lock_version = 1\nlock_version = 1\n" + entry, "duplicate top-level"},
		{"entry key", "lock_version = 1\n" + strings.Replace(entry, "configured =", "configured = \"node:24-slim\"\nconfigured =", 1), "duplicate lock key"},
		{"section", "lock_version = 1\n" + entry + entry, "duplicate image lock section"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseLockFile([]byte(tc.body), maxLockVersion); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("parse error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestWriteRejectsMalformedLocalImageID(t *testing.T) {
	lf := &LockFile{Version: 1, Images: map[string]LockEntry{
		"local/tool:dev": {
			Configured: "local/tool:dev",
			Resolved:   "sha256:not-an-image-id",
			Digest:     "sha256:not-an-image-id",
		},
	}}
	err := Write(filepath.Join(t.TempDir(), "container-bin.lock"), lf)
	if err == nil || !strings.Contains(err.Error(), "invalid local image ID") {
		t.Fatalf("Write error = %v, want invalid local image ID", err)
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
	if _, err := Load(path); err == nil {
		t.Fatal("expected wrong entry id rejection")
	}
}

func TestLockFileRejectsMutableOrForeignRepositoryResolution(t *testing.T) {
	configured := "ghcr.io/acme/tool:1"
	id := entryID(configured)
	cases := []string{
		"ghcr.io/acme/tool:latest",
		"ghcr.io/acme/tool:latest@sha256:" + strings.Repeat("a", 64),
		"ghcr.io/acme/tool@bad@sha256:" + strings.Repeat("a", 64),
		"evil.example/tool@sha256:" + strings.Repeat("a", 64),
		"ghcr.io/acme/tool@sha256:short",
	}
	for _, resolved := range cases {
		digest := "sha256:" + strings.Repeat("a", 64)
		contents := fmt.Sprintf("lock_version = 1\n[images.%s]\nconfigured = %q\nresolved = %q\ndigest = %q\n", id, configured, resolved, digest)
		path := filepath.Join(t.TempDir(), "container-bin.lock")
		if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Errorf("Load accepted unsafe resolved reference %q", resolved)
		}
	}
}

func TestConfiguredImagesIncludesNode22(t *testing.T) {
	reg := registry.Default()
	got := ConfiguredImages(reg)
	want := map[string]bool{
		"node:24-slim": true,
		"node:22-slim": true,
	}
	for image := range want {
		if !containsString(got, image) {
			t.Fatalf("ConfiguredImages missing %q; got %v", image, got)
		}
	}
	// node:22-slim and node:24-slim are distinct configured references, so they
	// must produce distinct lock entries.
	node24Idx, node22Idx := -1, -1
	for i, image := range got {
		if image == "node:24-slim" {
			node24Idx = i
		}
		if image == "node:22-slim" {
			node22Idx = i
		}
	}
	if node24Idx == -1 || node22Idx == -1 {
		t.Fatalf("missing node images in %v", got)
	}
	if node24Idx == node22Idx {
		t.Fatal("node:24-slim and node:22-slim collapsed into the same entry")
	}
}

func TestConfiguredImagesDeduplicatesRustToolchain(t *testing.T) {
	got := ConfiguredImages(registry.Default())
	const rustImage = "rust:1.98.1-slim-bookworm"
	count := 0
	for _, image := range got {
		if image == rustImage {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("ConfiguredImages contains %d copies of %q; got %v", count, rustImage, got)
	}
}

func TestConfiguredImagesDeduplicatesUVRuntime(t *testing.T) {
	got := ConfiguredImages(registry.Default())
	const uvImage = "ghcr.io/astral-sh/uv:0.12-python3.13-trixie-slim"
	count := 0
	for _, image := range got {
		if image == uvImage {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("ConfiguredImages contains %d copies of %q; got %v", count, uvImage, got)
	}
}

func TestConfiguredImagesIncludesDotnetSDK(t *testing.T) {
	got := ConfiguredImages(registry.Default())
	const image = "mcr.microsoft.com/dotnet/sdk:10.0"
	if !containsString(got, image) {
		t.Fatalf("ConfiguredImages missing %q; got %v", image, got)
	}
}

func TestConfiguredImagesDeduplicatesRubyRuntime(t *testing.T) {
	got := ConfiguredImages(registry.Default())
	const image = "ruby:4.0-trixie"
	count := 0
	for _, configured := range got {
		if configured == image {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("ConfiguredImages contains %d copies of %q; got %v", count, image, got)
	}
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func TestConfiguredImagesDeduplicatesSharedImage(t *testing.T) {
	reg := registry.Registry{Tools: map[string]registry.Tool{
		"node": {Image: "node:24-slim"},
		"npm":  {Image: "node:24-slim"},
		"jq":   {Image: "ghcr.io/jqlang/jq:latest"},
	}}
	got := ConfiguredImages(reg)
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

func TestLocalLockEntryUsesExactImageID(t *testing.T) {
	id := "sha256:" + strings.Repeat("c", 64)
	e, err := localLockEntry("local/tool:dev", imageInspection{
		id:          id,
		repoDigests: []string{"local/tool@" + id},
	})
	if err != nil {
		t.Fatal(err)
	}
	if e.Configured != "local/tool:dev" || e.Resolved != id || e.Digest != id {
		t.Fatalf("unexpected local lock entry: %#v", e)
	}
	if !IsLocalResolved(e.Resolved) {
		t.Fatalf("IsLocalResolved(%q) = false", e.Resolved)
	}
}

func TestLocalLockEntryRejectsMalformedImageID(t *testing.T) {
	for _, id := range []string{"", "sha256:short", "sha256:" + strings.Repeat("z", 64), strings.Repeat("a", 64)} {
		if _, err := localLockEntry("local/tool:dev", imageInspection{id: id}); err == nil {
			t.Errorf("localLockEntry accepted malformed ID %q", id)
		}
		if IsLocalResolved(id) {
			t.Errorf("IsLocalResolved accepted malformed ID %q", id)
		}
	}
}

func TestRepositoryLockEntryDoesNotTreatForeignDigestAsLocal(t *testing.T) {
	id := "sha256:" + strings.Repeat("d", 64)
	_, err := repositoryLockEntry("local/tool:dev", imageInspection{
		id:          id,
		repoDigests: []string{"someone/else@sha256:" + strings.Repeat("e", 64)},
	})
	if err == nil || !strings.Contains(err.Error(), "no RepoDigest for repository") {
		t.Fatalf("repositoryLockEntry error = %v, want repository mismatch", err)
	}
}

func TestResolveImagePolicyDenialHappensBeforeDocker(t *testing.T) {
	p := policy.Policy{SchemaVersion: 1, AllowedRepositories: []string{"ghcr.io/acme"}}
	if _, err := ResolveRepositoryImage("ghcr.io/other/tool:1", p); err == nil || !strings.Contains(err.Error(), "[policy.repository_denied]") {
		t.Fatalf("repository resolution error = %v", err)
	}
	if _, err := ResolveLocalImage("local/tool:dev", p); err == nil || !strings.Contains(err.Error(), "[policy.local_image_denied]") {
		t.Fatalf("local resolution error = %v", err)
	}
}

func TestRuntimeImageForToolReportsPolicyBeforeStaleLock(t *testing.T) {
	tool := registry.Tool{Name: "python", Image: "python:3.13"}
	stale := &LockFile{Version: 1, Images: map[string]LockEntry{}}

	requireLock := policy.Policy{SchemaVersion: 1, RequireLock: true}
	if _, err := runtimeImageForTool(tool, requireLock, stale, "container-bin.lock"); err == nil || !strings.Contains(err.Error(), "[policy.lock_required]") {
		t.Fatalf("require-lock stale entry error = %v", err)
	}

	denyRepository := policy.Policy{SchemaVersion: 1, AllowedRepositories: []string{"ghcr.io/acme"}}
	if _, err := runtimeImageForTool(tool, denyRepository, stale, "container-bin.lock"); err == nil || !strings.Contains(err.Error(), "[policy.repository_denied]") {
		t.Fatalf("repository-denied stale entry error = %v", err)
	}

	allowRepository := policy.Policy{SchemaVersion: 1, AllowedRepositories: []string{"docker.io/library"}}
	if _, err := runtimeImageForTool(tool, allowRepository, stale, "container-bin.lock"); err == nil || !strings.Contains(err.Error(), "is not locked") {
		t.Fatalf("authorized stale entry error = %v, want generic stale-lock error", err)
	}
}

func TestLoadLockFile_RecoversFromBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "container-bin.lock")
	bak := path + ".bak"
	lf := &LockFile{Version: 1, Images: map[string]LockEntry{
		"node:24-slim": {
			Configured: "node:24-slim",
			Resolved:   "node@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	}}
	if err := os.WriteFile(bak, render(lf), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("loadLockFile: %v", err)
	}
	if got == nil {
		t.Fatal("expected parsed lockfile, got nil")
	}
	if got.Version != 1 || len(got.Images) != 1 {
		t.Fatalf("unexpected lockfile: %#v", got)
	}
	e, ok := got.Images["node:24-slim"]
	if !ok || e.Resolved != lf.Images["node:24-slim"].Resolved {
		t.Fatalf("entry mismatch: %#v", e)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("live lockfile missing: %v", err)
	}
	if _, err := os.Stat(bak); !os.IsNotExist(err) {
		t.Fatalf("backup still exists: %v", err)
	}
}

func TestLoadLockFile_NoFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "container-bin.lock")
	got, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

func TestLoadLockFile_CorruptBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "container-bin.lock")
	bak := path + ".bak"
	if err := os.WriteFile(bak, []byte("not a lockfile\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for corrupt backup")
	}
	if _, err := os.Stat(bak); err != nil {
		t.Fatalf("backup missing after failed recovery: %v", err)
	}
}
