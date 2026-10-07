package wsllock

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
	"github.com/AviBackToBlack/container-bin/internal/lockfile"
	"github.com/AviBackToBlack/container-bin/internal/policy"
	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/wslfs"
)

func TestApplyPreauthorizesThenWritesOnePrivateCompleteLock(t *testing.T) {
	layout := testLayout()
	reg := testRegistry()
	deps := testCommand(layout, reg)
	var events []string
	deps.pullImage = func(_ context.Context, image string) error {
		events = append(events, "pull:"+image)
		return nil
	}
	deps.inspectImage = func(_ context.Context, image string) (imageIdentity, error) {
		events = append(events, "inspect:"+image)
		id := "sha256:" + strings.Repeat("a", 64)
		if image == "local/tool:dev" {
			return imageIdentity{id: id}, nil
		}
		return imageIdentity{id: id, repoDigests: []string{"python@sha256:" + strings.Repeat("b", 64)}}, nil
	}
	var written *lockfile.LockFile
	deps.writeLock = func(path string, candidate *lockfile.LockFile, mode os.FileMode) error {
		if path != layout.LockPath || mode != 0o600 {
			t.Fatalf("write = %q mode %04o", path, mode)
		}
		written = candidate
		events = append(events, "write")
		return nil
	}
	var out bytes.Buffer
	err := deps.run(context.Background(), []string{"--apply", "--local", "demo"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if written == nil || len(written.Images) != 2 {
		t.Fatalf("written lockfile = %#v", written)
	}
	if got := written.Images["local/tool:dev"].Resolved; got != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("local resolution = %q", got)
	}
	if got := written.Images["python:3.13-slim"].Resolved; got != "python@sha256:"+strings.Repeat("b", 64) {
		t.Fatalf("repository resolution = %q", got)
	}
	if strings.Join(events, ",") != "inspect:local/tool:dev,pull:python:3.13-slim,inspect:python:3.13-slim,write" {
		t.Fatalf("events = %v", events)
	}
	for _, want := range []string{"inspecting local local/tool:dev", "pulling  python:3.13-slim", "applied and revalidated"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output %q missing %q", out.String(), want)
		}
	}
}

func TestApplyAcceptsExplicitLocalImageIDWithoutRepositoryTrustLookup(t *testing.T) {
	layout := testLayout()
	id := "sha256:" + strings.Repeat("a", 64)
	reg := registry.Registry{Tools: map[string]registry.Tool{
		"local-id": {Name: "local-id", Image: id},
	}}
	deps := testCommand(layout, reg)
	deps.pullImage = func(context.Context, string) error {
		t.Fatal("explicit local image ID reached Docker pull")
		return nil
	}
	deps.inspectImage = func(_ context.Context, image string) (imageIdentity, error) {
		if image != id {
			t.Fatalf("inspect image = %q, want %q", image, id)
		}
		return imageIdentity{id: id}, nil
	}
	var written *lockfile.LockFile
	deps.writeLock = func(_ string, candidate *lockfile.LockFile, _ os.FileMode) error {
		written = candidate
		return nil
	}
	if err := deps.run(context.Background(), []string{"--apply", "--local", "local-id"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if written == nil || written.Images[id].Resolved != id {
		t.Fatalf("written lockfile = %#v, want explicit local ID", written)
	}
}

func TestApplyRejectsAllPolicyTargetsBeforeDocker(t *testing.T) {
	layout := testLayout()
	reg := registry.Registry{Tools: map[string]registry.Tool{
		"allowed": {Name: "allowed", Image: "ghcr.io/acme/allowed:1"},
		"denied":  {Name: "denied", Image: "ghcr.io/other/denied:1"},
	}}
	deps := testCommand(layout, reg)
	deps.loadPolicy = func() (policy.Policy, error) {
		return policy.Policy{SchemaVersion: 1, AllowedRepositories: []string{"ghcr.io/acme"}}, nil
	}
	deps.pullImage = func(context.Context, string) error {
		t.Fatal("Docker pull reached before complete policy authorization")
		return nil
	}
	err := deps.run(context.Background(), []string{"--apply"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "policy.repository_denied") {
		t.Fatalf("apply error = %v", err)
	}
}

func TestCheckIsReadOnlyAndAuthorizesBeforeInspect(t *testing.T) {
	layout := testLayout()
	reg := testRegistry()
	deps := testCommand(layout, reg)
	id := "sha256:" + strings.Repeat("a", 64)
	digest := "sha256:" + strings.Repeat("b", 64)
	deps.loadLockReadOnly = func(path string) (*lockfile.LockFile, error) {
		if path != layout.LockPath {
			t.Fatalf("lock path = %q", path)
		}
		return &lockfile.LockFile{Version: 1, Images: map[string]lockfile.LockEntry{
			"local/tool:dev": {Configured: "local/tool:dev", Resolved: id, Digest: id},
			"python:3.13-slim": {
				Configured: "python:3.13-slim",
				Resolved:   "python@" + digest,
				Digest:     digest,
			},
		}}, nil
	}
	var inspected []string
	deps.inspectImage = func(_ context.Context, reference string) (imageIdentity, error) {
		inspected = append(inspected, reference)
		return imageIdentity{id: id}, nil
	}
	deps.withLock = func(string, func() error) error { panic("read-only check acquired mutation lock") }
	deps.loadRegistry = func(string, registry.Authenticator) (registry.Registry, string, error) {
		panic("read-only check used recovering registry loader")
	}
	deps.loadLock = func(string) (*lockfile.LockFile, error) { panic("read-only check used recovering lock loader") }
	deps.pullImage = func(context.Context, string) error { panic("read-only check pulled image") }
	deps.writeLock = func(string, *lockfile.LockFile, os.FileMode) error { panic("read-only check wrote lock") }
	var out bytes.Buffer
	if err := deps.run(context.Background(), []string{"--check"}, &out); err != nil {
		t.Fatal(err)
	}
	if len(inspected) != 2 || !strings.Contains(out.String(), "read-only; no files changed") {
		t.Fatalf("inspected=%v output=%q", inspected, out.String())
	}
}

func TestCheckDoesNotInspectDeniedOrMissingEntries(t *testing.T) {
	layout := testLayout()
	reg := testRegistry()
	deps := testCommand(layout, reg)
	deps.loadPolicy = func() (policy.Policy, error) {
		return policy.Policy{SchemaVersion: 1, AllowLocalImages: false}, nil
	}
	id := "sha256:" + strings.Repeat("a", 64)
	deps.loadLockReadOnly = func(string) (*lockfile.LockFile, error) {
		return &lockfile.LockFile{Version: 1, Images: map[string]lockfile.LockEntry{
			"local/tool:dev": {Configured: "local/tool:dev", Resolved: id, Digest: id},
		}}, nil
	}
	deps.inspectImage = func(context.Context, string) (imageIdentity, error) {
		t.Fatal("denied or missing entry reached Docker inspection")
		return imageIdentity{}, nil
	}
	var out bytes.Buffer
	err := deps.run(context.Background(), []string{"--check"}, &out)
	if err == nil || !strings.Contains(err.Error(), "2 image(s)") || !strings.Contains(out.String(), "DENIED") || !strings.Contains(out.String(), "MISSING") {
		t.Fatalf("error=%v output=%q", err, out.String())
	}
}

func TestCheckMixedFailureStopsBeforeAnyDockerInspection(t *testing.T) {
	layout := testLayout()
	reg := registry.Registry{Tools: map[string]registry.Tool{
		"allowed": {Name: "allowed", Image: "ghcr.io/acme/allowed:1"},
		"denied":  {Name: "denied", Image: "ghcr.io/other/denied:1"},
	}}
	deps := testCommand(layout, reg)
	deps.loadPolicy = func() (policy.Policy, error) {
		return policy.Policy{SchemaVersion: 1, AllowedRepositories: []string{"ghcr.io/acme"}}, nil
	}
	digest := "sha256:" + strings.Repeat("b", 64)
	deps.loadLockReadOnly = func(string) (*lockfile.LockFile, error) {
		return &lockfile.LockFile{Version: 1, Images: map[string]lockfile.LockEntry{
			"ghcr.io/acme/allowed:1": {
				Configured: "ghcr.io/acme/allowed:1",
				Resolved:   "ghcr.io/acme/allowed@" + digest,
				Digest:     digest,
			},
			"ghcr.io/other/denied:1": {
				Configured: "ghcr.io/other/denied:1",
				Resolved:   "ghcr.io/other/denied@" + digest,
				Digest:     digest,
			},
		}}, nil
	}
	deps.inspectImage = func(context.Context, string) (imageIdentity, error) {
		t.Fatal("partially authorized set reached Docker inspection")
		return imageIdentity{}, nil
	}
	var out bytes.Buffer
	err := deps.run(context.Background(), []string{"--check"}, &out)
	if err == nil || !strings.Contains(err.Error(), "1 image(s) missing, unlocked, or denied") || !strings.Contains(out.String(), "DENIED") {
		t.Fatalf("error=%v output=%q", err, out.String())
	}
}

func TestRunRejectsUnsafeOrIncompleteInvocation(t *testing.T) {
	layout := testLayout()
	for _, args := range [][]string{nil, {"--apply", "--local"}, {"--check", "--local", "demo"}, {"--apply", "--other", "demo"}} {
		deps := testCommand(layout, testRegistry())
		if err := deps.run(context.Background(), args, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), usage) {
			t.Errorf("args %v error = %v", args, err)
		}
	}
	deps := testCommand(layout, testRegistry())
	if err := deps.run(nil, []string{"--check"}, &bytes.Buffer{}); err == nil {
		t.Fatal("nil context accepted")
	}
	if err := deps.run(context.Background(), []string{"--check"}, nil); err == nil {
		t.Fatal("nil writer accepted")
	}
	deps = testCommand(layout, testRegistry())
	deps.currentLayout = nil
	if err := deps.run(context.Background(), []string{"--check"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete command error = %v", err)
	}
}

func TestApplyRejectsUnknownLocalToolBeforeDocker(t *testing.T) {
	deps := testCommand(testLayout(), testRegistry())
	deps.pullImage = func(context.Context, string) error {
		t.Fatal("Docker pull reached for invalid local tool selection")
		return nil
	}
	err := deps.run(context.Background(), []string{"--apply", "--local", "missing"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "concrete configured tool") {
		t.Fatalf("apply error = %v", err)
	}
}

func testLayout() hostenv.WSLLayout {
	return hostenv.WSLLayout{
		Distro: "Ubuntu-24.04", UID: 1000, Home: "/home/alice",
		BinaryPath: "/home/alice/.local/lib/container-bin/cb", ManagementShim: "/home/alice/.local/bin/cb",
		ShimDir: "/home/alice/.local/bin", ConfigDir: "/home/alice/.config/container-bin",
		RegistryPath: "/home/alice/.config/container-bin/container-bin.toml",
		LockPath:     "/home/alice/.config/container-bin/container-bin.lock",
		StateDir:     "/home/alice/.local/state/container-bin", StateNamespace: "wsl2-0123456789abcdef0123456789abcdef",
	}
}

func testRegistry() registry.Registry {
	return registry.Registry{Tools: map[string]registry.Tool{
		"demo":   {Name: "demo", Image: "local/tool:dev"},
		"python": {Name: "python", Image: "python:3.13-slim"},
	}}
}

func testCommand(layout hostenv.WSLLayout, reg registry.Registry) command {
	return command{
		currentLayout: func() (hostenv.WSLLayout, error) { return layout, nil },
		checkLayout: func(got hostenv.WSLLayout) (wslfs.Plan, error) {
			return wslfs.Plan{Layout: got}, nil
		},
		checkRegistryRecovery: func(hostenv.WSLLayout) error { return nil },
		checkLockRecovery:     func(hostenv.WSLLayout) error { return nil },
		loadPolicy:            func() (policy.Policy, error) { return policy.Policy{}, nil },
		loadRegistry: func(path string, _ registry.Authenticator) (registry.Registry, string, error) {
			return reg, path, nil
		},
		loadRegistryReadOnly: func(path string, _ registry.Authenticator) (registry.Registry, string, error) {
			return reg, path, nil
		},
		loadLock:         func(string) (*lockfile.LockFile, error) { return nil, nil },
		loadLockReadOnly: func(string) (*lockfile.LockFile, error) { return nil, nil },
		pullImage:        func(context.Context, string) error { return nil },
		inspectImage: func(context.Context, string) (imageIdentity, error) {
			return imageIdentity{}, errors.New("unexpected inspect")
		},
		writeLock: func(string, *lockfile.LockFile, os.FileMode) error { return nil },
		withLock: func(path string, fn func() error) error {
			if path != layout.RegistryPath {
				return errors.New("unexpected mutation lock path")
			}
			return fn()
		},
	}
}
