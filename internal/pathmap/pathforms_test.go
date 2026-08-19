package pathmap

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/registry"
)

func TestPathFormP1JunctionToRoot(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows path-mapping semantics")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatalf("P1 MkdirAll: %v", err)
	}
	mustWriteFile(t, filepath.Join(target, "a.txt"), []byte{})

	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("P1: creating directory symlink requires privilege or developer mode: %v", err)
	}

	through := mustCanonical(t, link)
	resolved := mustCanonical(t, target)
	if through != resolved {
		t.Fatalf("P1: canonical through link %q != resolved %q", through, resolved)
	}

	tool := registry.Tool{Name: "demo"}
	arg := filepath.Join(link, "a.txt")
	mapped, mounts, err := MapToolArgs(tool, resolved, resolved, "/workspace", []string{arg})
	if err != nil {
		t.Fatalf("P1: err=%v", err)
	}
	want := []string{"/workspace/a.txt"}
	if !reflect.DeepEqual(mapped, want) {
		t.Fatalf("P1: mapped=%#v, want %#v", mapped, want)
	}
	if len(mounts) != 0 {
		t.Fatalf("P1: expected no mounts, got %#v", mounts)
	}
}

func TestPathFormP2JunctionEscapesRoot(t *testing.T) {
	root, ext := windowsFixtures(t)

	escape := filepath.Join(root, "escape")
	if err := os.Symlink(ext, escape); err != nil {
		t.Skipf("P2: creating directory symlink requires privilege or developer mode: %v", err)
	}

	tool := registry.Tool{Name: "demo"}
	arg := `.\escape\b.txt`
	mapped, mounts, err := MapToolArgs(tool, root, root, "/workspace", []string{arg})
	if err != nil {
		t.Fatalf("P2: err=%v", err)
	}
	if len(mapped) != 1 || mapped[0] != "/cb/mounts/0/b.txt" {
		t.Fatalf("P2: mapped=%#v, want %#v", mapped, []string{"/cb/mounts/0/b.txt"})
	}
	if len(mounts) != 1 || mounts[0].Source != ext || mounts[0].Target != "/cb/mounts/0" {
		t.Fatalf("P2: mounts=%#v", mounts)
	}
}

func TestPathFormP5CaseInsensitiveEquivalence(t *testing.T) {
	root, ext := windowsFixtures(t)
	tool := registry.Tool{Name: "demo"}

	upperIn := filepath.Join(strings.ToUpper(root), "SUB", "A.TXT")
	mapped, mounts, err := MapToolArgs(tool, root, root, "/workspace", []string{upperIn})
	if err != nil {
		t.Fatalf("P5 in-root: err=%v", err)
	}
	if !reflect.DeepEqual(mapped, []string{"/workspace/sub/a.txt"}) {
		t.Fatalf("P5 in-root: mapped=%#v", mapped)
	}
	if len(mounts) != 0 {
		t.Fatalf("P5 in-root: expected no mounts, got %#v", mounts)
	}

	upperExt := filepath.Join(strings.ToUpper(ext), "B.TXT")
	lowerExt := filepath.Join(ext, "b.txt")
	mapped, mounts, err = MapToolArgs(tool, root, root, "/workspace", []string{upperExt, lowerExt})
	if err != nil {
		t.Fatalf("P5 external: err=%v", err)
	}
	if !reflect.DeepEqual(mapped, []string{"/cb/mounts/0/b.txt", "/cb/mounts/0/b.txt"}) {
		t.Fatalf("P5 external: mapped=%#v", mapped)
	}
	if len(mounts) != 1 || mounts[0].Source != ext || mounts[0].Target != "/cb/mounts/0" {
		t.Fatalf("P5 external: mounts=%#v", mounts)
	}
}

func TestPathFormP6WorkspaceBasenameLowercased(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows path-mapping semantics")
	}

	dir := t.TempDir()
	raw := filepath.Join(dir, "My-App")
	if err := os.MkdirAll(raw, 0755); err != nil {
		t.Fatalf("P6 MkdirAll: %v", err)
	}
	mustWriteFile(t, filepath.Join(raw, "x.txt"), []byte{})
	canon := mustCanonical(t, raw)

	got := WorkspaceRootFor(registry.Tool{Provider: "stateful"}, canon)
	want := "/workspace/my-app"
	if got != want {
		t.Fatalf("P6: WorkspaceRootFor(%q) = %q, want %q", canon, got, want)
	}
}

func TestPathFormP7UNCArgumentDeclined(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows path-mapping semantics")
	}
	root, _ := windowsFixtures(t)
	tool := registry.Tool{Name: "demo"}
	arg := `\\server\share\x`

	if isWindowsAbsPath(arg) {
		t.Fatalf("P7: isWindowsAbsPath(%q) unexpectedly true", arg)
	}
	in := []string{arg}
	mapped, mounts, err := MapToolArgs(tool, root, root, "/workspace", in)
	if err != nil {
		t.Fatalf("P7: err=%v", err)
	}
	if !reflect.DeepEqual(mapped, in) {
		t.Fatalf("P7: mapped=%#v, want unchanged %#v", mapped, in)
	}
	if len(mounts) != 0 {
		t.Fatalf("P7: expected no mounts, got %#v", mounts)
	}
}

// The decline in TestPathFormP7UNCArgumentDeclined is not solely due to
// isWindowsAbsPath. The third classifier branch joins any backslash-containing
// argument onto cwd and stats it, and filepath.Join collapses the leading
// separators, so `\\server\share\x` probes `<cwd>\server\share\x`. Where that
// subtree happens to exist the argument *is* mapped -- to an unrelated local
// directory. Far-fetched, but it is the real behavior, so it is pinned here
// rather than left to be rediscovered.
func TestPathFormP7UNCCoincidentalSubtreeIsMapped(t *testing.T) {
	root, _ := windowsFixtures(t)
	mustWriteFile(t, filepath.Join(root, "server", "share", "x"), []byte{})

	tool := registry.Tool{Name: "demo"}
	mapped, mounts, err := MapToolArgs(tool, root, root, "/workspace", []string{`\\server\share\x`})
	if err != nil {
		t.Fatalf("P7 coincidental: err=%v", err)
	}
	want := []string{"/workspace/server/share/x"}
	if !reflect.DeepEqual(mapped, want) {
		t.Fatalf("P7 coincidental: mapped=%#v, want %#v", mapped, want)
	}
	if len(mounts) != 0 {
		t.Fatalf("P7 coincidental: expected no mounts, got %#v", mounts)
	}
}

func TestPathFormP9LongPathSyntaxDeclined(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows path-mapping semantics")
	}
	root, _ := windowsFixtures(t)
	tool := registry.Tool{Name: "demo"}

	cases := []struct {
		name string
		arg  string
	}{
		{"drive", `\\?\C:\Windows`},
		{"unc", `\\?\UNC\server\share\x`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if isWindowsAbsPath(tc.arg) {
				t.Fatalf("P9 %s: isWindowsAbsPath(%q) unexpectedly true", tc.name, tc.arg)
			}
			in := []string{tc.arg}
			mapped, mounts, err := MapToolArgs(tool, root, root, "/workspace", in)
			if err != nil {
				t.Fatalf("P9 %s: err=%v", tc.name, err)
			}
			if !reflect.DeepEqual(mapped, in) {
				t.Fatalf("P9 %s: mapped=%#v, want unchanged %#v", tc.name, mapped, in)
			}
			if len(mounts) != 0 {
				t.Fatalf("P9 %s: expected no mounts, got %#v", tc.name, mounts)
			}
		})
	}
}

func TestPathFormP11TrailingDotsAndSpaces(t *testing.T) {
	root, _ := windowsFixtures(t)
	mustWriteFile(t, filepath.Join(root, "foo"), []byte{})
	plain := mustCanonical(t, filepath.Join(root, "foo"))

	// filepath.Abs goes through GetFullPathNameW on Windows for absolute and
	// relative inputs alike, so no chdir is needed to exercise the stripping.
	cases := []struct {
		name string
		arg  string
	}{
		{"trailing dot", filepath.Join(root, "foo.")},
		{"trailing space", filepath.Join(root, "foo ")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CanonicalPath(tc.arg)
			if err != nil {
				t.Fatalf("P11 %s: CanonicalPath(%q): %v", tc.name, tc.arg, err)
			}
			if got != plain {
				t.Fatalf("P11 %s: canonical=%q, want %q", tc.name, got, plain)
			}
		})
	}

	// End to end, in the explicit-relative shape a user actually types: the
	// argument reaches the tool as the stripped name, not as a literal.
	tool := registry.Tool{Name: "demo"}
	in := []string{`.\foo.`}
	mapped, mounts, err := MapToolArgs(tool, root, root, "/workspace", in)
	if err != nil {
		t.Fatalf("P11 end-to-end: err=%v", err)
	}
	if !reflect.DeepEqual(mapped, []string{"/workspace/foo"}) {
		t.Fatalf("P11 end-to-end: mapped=%#v, want %#v", mapped, []string{"/workspace/foo"})
	}
	if len(mounts) != 0 {
		t.Fatalf("P11 end-to-end: expected no mounts, got %#v", mounts)
	}
}

func TestPathFormP13DeclinedArgumentsPassByteForByte(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows path-mapping semantics")
	}
	root, _ := windowsFixtures(t)
	tool := registry.Tool{Name: "demo"}

	cases := []struct {
		name string
		arg  string
	}{
		{"wildcard", `.\cmd\...`},
		{"unc", `\\server\share\x`},
		{"missing-backslash", `nope\missing`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := []string{tc.arg}
			mapped, mounts, err := MapToolArgs(tool, root, root, "/workspace", in)
			if err != nil {
				t.Fatalf("P13 %s: err=%v", tc.name, err)
			}
			if !reflect.DeepEqual(mapped, in) {
				t.Fatalf("P13 %s: mapped=%#v, want unchanged %#v", tc.name, mapped, in)
			}
			if len(mounts) != 0 {
				t.Fatalf("P13 %s: expected no mounts, got %#v", tc.name, mounts)
			}
		})
	}
}
