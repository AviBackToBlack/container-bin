package projectconfig

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/pathmap"
	"github.com/AviBackToBlack/container-bin/internal/policy"
	"github.com/AviBackToBlack/container-bin/internal/registry"
)

const globalRegistry = `schema_version = 2

[defaults.node]
version = "22"

[tools.node22]
image = "node:22-slim"
provider = "stateful"
state_group = "node22"
shared_volumes = ["cache:/root/.npm"]
default_family = "node"
default_version = "22"
default_alias = "node"
`

const validOverlay = `schema_version = 2

[tools.acme]
image = "ghcr.io/acme/tool:v1"
provider = "stateful"
state_group = "acme"
project_volumes = ["state:/workspace/.acme"]
env_names = ["ACME_TOKEN"]
env_set = ["ACME_MODE=review"]
`

func mustRegistry(t *testing.T, text string) registry.Registry {
	t.Helper()
	reg, err := registry.ParseTOML(text)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func writeOverlay(t *testing.T, root, text string) string {
	t.Helper()
	path := filepath.Join(root, Filename)
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFindUsesCanonicalAncestor(t *testing.T) {
	root := t.TempDir()
	writeOverlay(t, root, validOverlay)
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	got, found, err := Find(nested)
	if err != nil || !found {
		t.Fatalf("Find() = (%+v, %t, %v)", got, found, err)
	}
	want, err := pathmap.CanonicalPath(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != want || got.Path != filepath.Join(want, Filename) {
		t.Fatalf("Find() = %+v, want root %q", got, want)
	}
}

func TestInspectDefaultSkipsUserConfigLookupWithoutOverlay(t *testing.T) {
	for _, name := range []string{"APPDATA", "HOME", "XDG_CONFIG_HOME"} {
		t.Setenv(name, "")
	}
	ctx, trustPath := InspectDefault(mustRegistry(t, globalRegistry), t.TempDir())
	if ctx.Status != Absent || ctx.Err != nil || trustPath != "" {
		t.Fatalf("InspectDefault() = status %q, path %q, error %v", ctx.Status, trustPath, ctx.Err)
	}
	if err := UntrustDefault(t.TempDir(), ioDiscard{}); err == nil || !strings.Contains(err.Error(), "no .container-bin.toml") {
		t.Fatalf("UntrustDefault() error = %v, want absent-overlay diagnostic", err)
	}
}

func TestFindRejectsSymlinkOverlay(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is privilege-dependent on Windows")
	}
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "overlay.toml")
	if err := os.WriteFile(target, []byte(validOverlay), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, Filename)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Find(root); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("Find() error = %v, want regular-file rejection", err)
	}
}

func TestFindRequiresCanonicalIdentityOnlyWhenOverlayExists(t *testing.T) {
	resolveErr := errors.New("unresolvable reparse point")
	resolve := func(string) (string, error) { return "", resolveErr }
	empty := t.TempDir()
	if got, found, err := find(empty, resolve); err != nil || found {
		t.Fatalf("find(no overlay) = (%+v, %t, %v), want absent without error", got, found, err)
	}
	root := t.TempDir()
	writeOverlay(t, root, validOverlay)
	if _, _, err := find(root, resolve); err == nil || !strings.Contains(err.Error(), "for overlay trust") {
		t.Fatalf("find(overlay) error = %v, want canonical-identity rejection", err)
	}
}

func TestLoadRejectsBroadOverlayCapabilities(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{name: "empty", body: "schema_version = 2\n", want: "registry contains no tools"},
		{
			name: "defaults",
			body: `schema_version = 2
[defaults.acme]
version = "1"
[tools.acme1]
image = "example.com/acme:v1"
default_family = "acme"
default_version = "1"
default_alias = "acme"
`,
			want: "sections are not allowed",
		},
		{name: "default metadata", body: strings.Replace(validOverlay, "provider = \"stateful\"", "provider = \"stateful\"\ndefault_family = \"acme\"\ndefault_version = \"1\"\ndefault_alias = \"acme-default\"", 1), want: "require a [defaults.acme] section"},
		{name: "host mounts", body: strings.Replace(validOverlay, "env_names =", `host_mounts = ["C:\\safe:/host:ro"]
env_names =`, 1), want: "host_mounts are not allowed"},
		{name: "environment prefixes", body: strings.Replace(validOverlay, "env_names =", `env_prefixes = ["ACME_"]
env_names =`, 1), want: "env_prefixes are not allowed"},
		{name: "shared volumes", body: strings.Replace(validOverlay, "project_volumes =", `shared_volumes = ["cache:/root/.cache"]
project_volumes =`, 1), want: "shared_volumes are not allowed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := writeOverlay(t, root, tc.body)
			_, err := Load(Location{Root: root, Path: path})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestMergeIsAddOnlyAcrossConcreteToolsAndAliases(t *testing.T) {
	global := mustRegistry(t, globalRegistry)
	root := t.TempDir()
	path := writeOverlay(t, root, validOverlay)
	overlay, err := Load(Location{Root: root, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	merged, err := Merge(global, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := merged.Resolve("acme"); !ok {
		t.Fatal("merged registry does not resolve project tool")
	}
	if got := merged.Tools["acme"].TrustedProjectRoot; got != root {
		t.Fatalf("merged project root = %q, want %q", got, root)
	}

	for _, name := range []string{"node", "node22"} {
		path = writeOverlay(t, root, strings.Replace(validOverlay, "tools.acme", "tools."+name, 1))
		overlay, err = Load(Location{Root: root, Path: path})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Merge(global, overlay); err == nil || !strings.Contains(err.Error(), "collides") {
			t.Fatalf("Merge(%q) error = %v, want collision", name, err)
		}
	}
}

func TestTrustLifecycleBindsCanonicalRootAndExactBytes(t *testing.T) {
	global := mustRegistry(t, globalRegistry)
	root := t.TempDir()
	writeOverlay(t, root, validOverlay)
	trustPath := filepath.Join(t.TempDir(), "trust.toml")
	ctx := Inspect(global, root, trustPath)
	if ctx.Status != Untrusted {
		t.Fatalf("initial status = %q, want %q (%v)", ctx.Status, Untrusted, ctx.Err)
	}

	installed := []string(nil)
	install := func(names []string) error { installed = append([]string(nil), names...); return nil }
	var out bytes.Buffer
	if err := Trust(ctx, trustPath, []string{"--check"}, strings.NewReader(""), &out, false, policy.Policy{}, install); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"project overlay:",
		"canonical root:",
		"digest:         sha256:",
		"machine policy:",
		`shim: "acme.exe"`,
		`image: "ghcr.io/acme/tool:v1"`,
		`env_names: ["ACME_TOKEN"]`,
		`env_set: ["ACME_MODE=review"]`,
		"path_last_if_any:",
		"project_root_mode:",
		`project_volumes: ["state:/workspace/.acme"]`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("trust review output does not contain %q:\n%s", want, out.String())
		}
	}
	if installed != nil {
		t.Fatalf("--check installed shims: %v", installed)
	}
	if _, err := os.Stat(trustPath); !os.IsNotExist(err) {
		t.Fatalf("--check created trust store: %v", err)
	}
	if err := Trust(ctx, trustPath, nil, strings.NewReader(""), &out, false, policy.Policy{}, install); err == nil || !strings.Contains(err.Error(), "non-interactive") {
		t.Fatalf("non-interactive Trust() error = %v", err)
	}
	if err := Trust(ctx, trustPath, []string{"--yes"}, strings.NewReader(""), &out, false, policy.Policy{}, install); err != nil {
		t.Fatal(err)
	}
	if strings.Join(installed, ",") != "acme" {
		t.Fatalf("installed shims = %v", installed)
	}
	if got := Inspect(global, root, trustPath); got.Status != Trusted {
		t.Fatalf("trusted status = %q (%v)", got.Status, got.Err)
	}
	moved := t.TempDir()
	writeOverlay(t, moved, validOverlay)
	if got := Inspect(global, moved, trustPath); got.Status != Untrusted {
		t.Fatalf("moved project status = %q, want %q (%v)", got.Status, Untrusted, got.Err)
	}

	path := filepath.Join(root, Filename)
	if err := os.WriteFile(path, []byte(validOverlay+"# review note\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := Inspect(global, root, trustPath); got.Status != Changed {
		t.Fatalf("changed status = %q (%v)", got.Status, got.Err)
	}
	if err := Untrust(root, trustPath, &out); err != nil {
		t.Fatal(err)
	}
	if got := Inspect(global, root, trustPath); got.Status != Untrusted {
		t.Fatalf("status after untrust = %q (%v)", got.Status, got.Err)
	}
}

func TestPrintReviewEscapesProjectControlledValues(t *testing.T) {
	root := t.TempDir()
	body := strings.Replace(validOverlay, "provider = \"stateful\"", `provider = "stateful"
args_prefix = ["safe\ntrust status: trusted"]`, 1)
	writeOverlay(t, root, body)
	ctx := Inspect(mustRegistry(t, globalRegistry), root, filepath.Join(t.TempDir(), "trust.toml"))
	var out bytes.Buffer
	if err := ctx.PrintReview(&out, policy.Policy{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"safe\ntrust status: trusted"`) {
		t.Fatalf("review did not quote escaped newline:\n%s", out.String())
	}
	if strings.Contains(out.String(), "safe\ntrust status: trusted") {
		t.Fatalf("review emitted a literal project-controlled newline:\n%s", out.String())
	}
}

func TestTrustStopsBeforeMutationWhenReviewOutputFails(t *testing.T) {
	root := t.TempDir()
	writeOverlay(t, root, validOverlay)
	trustPath := filepath.Join(t.TempDir(), "trust.toml")
	ctx := Inspect(mustRegistry(t, globalRegistry), root, trustPath)
	installed := false
	err := Trust(ctx, trustPath, []string{"--yes"}, strings.NewReader(""), failingWriter{}, false, policy.Policy{}, func([]string) error {
		installed = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "write project overlay review") {
		t.Fatalf("Trust() error = %v, want review-output failure", err)
	}
	if installed {
		t.Fatal("Trust() installed shims after review-output failure")
	}
	if _, err := os.Stat(trustPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Trust() wrote store after review-output failure: %v", err)
	}
}

func TestTrustInteractiveRequiresExactConfirmation(t *testing.T) {
	global := mustRegistry(t, globalRegistry)
	root := t.TempDir()
	writeOverlay(t, root, validOverlay)
	ctx := Inspect(global, root, filepath.Join(t.TempDir(), "missing.toml"))
	install := func([]string) error { return nil }
	for _, confirmation := range []string{"yes\n", " trust \n", "TRUST\n"} {
		if err := Trust(ctx, filepath.Join(t.TempDir(), "no.toml"), nil, strings.NewReader(confirmation), ioDiscard{}, true, policy.Policy{}, install); err == nil {
			t.Fatalf("Trust() accepted confirmation %q other than exact 'trust'", confirmation)
		}
	}
	trustPath := filepath.Join(t.TempDir(), "trust.toml")
	if err := Trust(ctx, trustPath, nil, strings.NewReader("trust\n"), ioDiscard{}, true, policy.Policy{}, install); err != nil {
		t.Fatal(err)
	}
}

func TestTrustRechecksOverlayBytesAfterInteractiveApproval(t *testing.T) {
	root := t.TempDir()
	path := writeOverlay(t, root, validOverlay)
	trustPath := filepath.Join(t.TempDir(), "trust.toml")
	ctx := Inspect(mustRegistry(t, globalRegistry), root, trustPath)
	input := strings.NewReader("trust\n")
	changed := false
	reader := readerFunc(func(p []byte) (int, error) {
		if !changed {
			changed = true
			if err := os.WriteFile(path, []byte(validOverlay+"# changed during prompt\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return input.Read(p)
	})
	installed := false
	err := Trust(ctx, trustPath, nil, reader, ioDiscard{}, true, policy.Policy{}, func([]string) error {
		installed = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "changed during review") {
		t.Fatalf("Trust() error = %v, want changed-during-review refusal", err)
	}
	if installed {
		t.Fatal("Trust() installed shims after the approved bytes changed")
	}
	if _, err := os.Stat(trustPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Trust() recorded stale bytes: %v", err)
	}
}

func TestTrustRechecksOverlayBytesAfterShimInstallation(t *testing.T) {
	root := t.TempDir()
	path := writeOverlay(t, root, validOverlay)
	trustPath := filepath.Join(t.TempDir(), "trust.toml")
	ctx := Inspect(mustRegistry(t, globalRegistry), root, trustPath)
	installed := false
	err := Trust(ctx, trustPath, []string{"--yes"}, strings.NewReader(""), ioDiscard{}, false, policy.Policy{}, func([]string) error {
		installed = true
		return os.WriteFile(path, []byte(validOverlay+"# changed while shims installed\n"), 0o600)
	})
	if err == nil || !strings.Contains(err.Error(), "shims were installed but remain inert") || !strings.Contains(err.Error(), "changed during review") {
		t.Fatalf("Trust() error = %v, want post-install change refusal", err)
	}
	if !installed {
		t.Fatal("test did not reach shim installation")
	}
	if _, err := os.Stat(trustPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Trust() recorded bytes changed during shim installation: %v", err)
	}
}

func TestTrustStoreStrictRoundTrip(t *testing.T) {
	root, err := pathmap.CanonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entry := trustEntry{Root: root, Digest: strings.Repeat("a", 64), Tools: []string{"acme", "zeta"}}
	store := trustStore{Entries: map[string]trustEntry{rootID(root): entry}}
	path := filepath.Join(t.TempDir(), "trust.toml")
	if err := saveStore(path, store); err != nil {
		t.Fatal(err)
	}
	got, err := loadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(got.Entries[rootID(root)].Tools, entry.Tools) {
		t.Fatalf("round-trip entry = %+v", got.Entries[rootID(root)])
	}
	if err := os.WriteFile(path, []byte("trust_version = 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadStore(path); err == nil || !strings.Contains(err.Error(), "unsupported trust_version") {
		t.Fatalf("loadStore() error = %v", err)
	}
}

func TestTrustStoreRejectsAmbiguousOrIncompleteState(t *testing.T) {
	root, err := pathmap.CanonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entry := trustEntry{Root: root, Digest: strings.Repeat("a", 64), Tools: []string{"acme", "zeta"}}
	data, err := marshalStore(trustStore{Entries: map[string]trustEntry{rootID(root): entry}})
	if err != nil {
		t.Fatal(err)
	}
	valid := string(data)
	tests := []struct {
		name, body, want string
	}{
		{name: "duplicate version", body: "trust_version = 1\n" + valid, want: "only one trust_version"},
		{name: "unknown key", body: strings.Replace(valid, "tools =", "mystery = true\ntools =", 1), want: "unsupported project trust key"},
		{name: "missing tools", body: strings.Replace(valid, "tools = [\"acme\", \"zeta\"]\n", "", 1), want: "at least one tool"},
		{name: "unsorted tools", body: strings.Replace(valid, "[\"acme\", \"zeta\"]", "[\"zeta\", \"acme\"]", 1), want: "tools must be sorted"},
		{name: "mismatched root ID", body: strings.Replace(valid, rootID(root), strings.Repeat("b", 64), 1), want: "does not match its canonical root"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseStore([]byte(tc.body)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("parseStore() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestTrustStoreBackupReadIsReadOnlyAndMutationRecovers(t *testing.T) {
	root, err := pathmap.CanonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entry := trustEntry{Root: root, Digest: strings.Repeat("a", 64), Tools: []string{"acme"}}
	data, err := marshalStore(trustStore{Entries: map[string]trustEntry{rootID(root): entry}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "trust.toml")
	backup := path + ".bak"
	if err := os.WriteFile(backup, data, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := loadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(got.Entries[rootID(root)].Tools, entry.Tools) {
		t.Fatalf("backup entry = %+v", got.Entries[rootID(root)])
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only load created primary store: %v", err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("read-only load removed backup: %v", err)
	}
	if _, err := loadStoreForMutation(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("mutation load did not recover primary: %v", err)
	}
	if _, err := os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mutation load left backup in place: %v", err)
	}
}

func TestTrustRejectsStoreInsideProject(t *testing.T) {
	global := mustRegistry(t, globalRegistry)
	root := t.TempDir()
	writeOverlay(t, root, validOverlay)
	insidePath := filepath.Join(root, "trust.toml")
	inside := Inspect(global, root, insidePath)
	if inside.Status != Invalid || inside.Err == nil || !strings.Contains(inside.Err.Error(), "inside project root") {
		t.Fatalf("inside-root Inspect() = status %q, error %v", inside.Status, inside.Err)
	}
	ctx := Inspect(global, root, filepath.Join(t.TempDir(), "missing.toml"))
	install := func([]string) error { return nil }
	if err := Trust(ctx, insidePath, []string{"--yes"}, strings.NewReader(""), ioDiscard{}, false, policy.Policy{}, install); err == nil || !strings.Contains(err.Error(), "inside project root") {
		t.Fatalf("inside-root Trust() error = %v", err)
	}
	if err := Untrust(root, insidePath, ioDiscard{}); err == nil || !strings.Contains(err.Error(), "inside project root") {
		t.Fatalf("inside-root Untrust() error = %v", err)
	}
}

func TestTrustRejectsExternalLookingSymlinkIntoProject(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is privilege-dependent on Windows")
	}
	base := t.TempDir()
	root := filepath.Join(base, "project")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	writeOverlay(t, root, validOverlay)
	alias := filepath.Join(base, "trust-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	ctx := Inspect(mustRegistry(t, globalRegistry), root, filepath.Join(t.TempDir(), "missing.toml"))
	inside := Inspect(mustRegistry(t, globalRegistry), root, filepath.Join(alias, "trust.toml"))
	if inside.Status != Invalid || inside.Err == nil || !strings.Contains(inside.Err.Error(), "inside project root") {
		t.Fatalf("symlinked inside-root Inspect() = status %q, error %v", inside.Status, inside.Err)
	}
	err := Trust(ctx, filepath.Join(alias, "trust.toml"), []string{"--yes"}, strings.NewReader(""), ioDiscard{}, false, policy.Policy{}, func([]string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "inside project root") {
		t.Fatalf("Trust() error = %v, want resolved containment rejection", err)
	}
	if err := Untrust(root, filepath.Join(alias, "trust.toml"), ioDiscard{}); err == nil || !strings.Contains(err.Error(), "inside project root") {
		t.Fatalf("Untrust() error = %v, want resolved containment rejection", err)
	}
}

func TestInspectRejectsExternalBackupSymlinkIntoProject(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is privilege-dependent on Windows")
	}
	root := t.TempDir()
	writeOverlay(t, root, validOverlay)
	projectControlled := filepath.Join(root, "fake-trust.toml")
	if err := os.WriteFile(projectControlled, []byte("trust_version = 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(t.TempDir(), "trust.toml")
	if err := os.Symlink(projectControlled, storePath+".bak"); err != nil {
		t.Fatal(err)
	}
	ctx := Inspect(mustRegistry(t, globalRegistry), root, storePath)
	if ctx.Status != Invalid || ctx.Err == nil || !strings.Contains(ctx.Err.Error(), "inside project root") {
		t.Fatalf("Inspect() = status %q, error %v; want backup containment rejection", ctx.Status, ctx.Err)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("output closed") }

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }
