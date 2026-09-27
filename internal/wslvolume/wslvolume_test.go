package wslvolume

import (
	"reflect"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

const testNamespace = "wsl2-0123456789abcdef0123456789abcdef"

func TestScopeBuildsNamespacedSharedVolume(t *testing.T) {
	scope := testScope(t)
	volume, err := scope.Shared("node24", "npm-cache")
	if err != nil {
		t.Fatal(err)
	}
	if volume.Name() != "cb-"+testNamespace+"-6-node24-9-npm-cache" {
		t.Fatalf("Shared() name = %q", volume.Name())
	}
	wantLabels := map[string]string{
		"cb.managed": "true", "cb.kind": "shared", "cb.owner": "node24/npm-cache",
		NamespaceLabel: testNamespace,
	}
	if !reflect.DeepEqual(volume.Labels(), wantLabels) {
		t.Fatalf("Shared() labels = %#v, want %#v", volume.Labels(), wantLabels)
	}
	if !volume.Matches(volume.Name(), volume.Labels()) {
		t.Fatal("Volume does not recognize its exact shared identity")
	}
}

func TestScopeBuildsCaseSensitiveProjectVolume(t *testing.T) {
	scope := testScope(t)
	lower, err := scope.Project("node24", "node-modules", "/home/alice/project")
	if err != nil {
		t.Fatal(err)
	}
	upper, err := scope.Project("node24", "node-modules", "/home/alice/Project")
	if err != nil {
		t.Fatal(err)
	}
	lowerLabels, upperLabels := lower.Labels(), upper.Labels()
	if lower.Name() == upper.Name() || lowerLabels["cb.project_hash"] == upperLabels["cb.project_hash"] {
		t.Fatal("case-distinct Linux project roots share volume identity")
	}
	if lowerLabels["cb.project_path"] != "/home/alice/project" || !strings.HasSuffix(lower.Name(), "-"+lowerLabels["cb.project_hash"]) {
		t.Fatalf("Project() = %#v", lower)
	}
	if lowerLabels["cb.project_hash"] != "e9b06e6a6ea9" {
		t.Fatalf("stable project hash = %q", lowerLabels["cb.project_hash"])
	}
	if !lower.Matches(lower.Name(), lowerLabels) {
		t.Fatal("Volume does not recognize its exact project identity")
	}
}

func TestScopeRequiresNameAndLabelNamespaceProof(t *testing.T) {
	scope := testScope(t)
	volume, err := scope.Shared("go124", "gomodcache")
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Volume){
		"wrong prefix": func(v *Volume) { v.name = "cb-other-go124-gomodcache" },
		"prefix only":  func(v *Volume) { v.name = scope.Prefix() },
		"unmanaged":    func(v *Volume) { delete(v.labels, "cb.managed") },
		"wrong owner":  func(v *Volume) { v.labels["cb.owner"] = "go124/other" },
		"wrong label":  func(v *Volume) { v.labels[NamespaceLabel] = "wsl2-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" },
		"extra label":  func(v *Volume) { v.labels["cb.unexpected"] = "true" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := Volume{name: volume.Name(), labels: volume.Labels()}
			mutate(&candidate)
			if volume.Matches(candidate.name, candidate.labels) {
				t.Fatal("Volume accepted an inexact identity")
			}
		})
	}
}

func TestOwnerEncodingIsUnambiguous(t *testing.T) {
	scope := testScope(t)
	first, err := scope.Shared("a-b", "c")
	if err != nil {
		t.Fatal(err)
	}
	second, err := scope.Shared("a", "b-c")
	if err != nil {
		t.Fatal(err)
	}
	if first.Name() == second.Name() {
		t.Fatalf("distinct owners collided at %q", first.Name())
	}
	if first.Matches(second.Name(), second.Labels()) || second.Matches(first.Name(), first.Labels()) {
		t.Fatal("one owner accepted another owner's identity")
	}
}

func TestScopesRemainIndependent(t *testing.T) {
	first := testScope(t)
	second, err := New(hostenv.WSLLayout{StateNamespace: "wsl2-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	volume, err := first.Project("node24", "node-modules", "/home/alice/project")
	if err != nil {
		t.Fatal(err)
	}
	other, err := second.Project("node24", "node-modules", "/home/alice/project")
	if err != nil {
		t.Fatal(err)
	}
	if volume.Name() == other.Name() || volume.Matches(other.Name(), other.Labels()) {
		t.Fatal("distinct WSL scopes produced the same project volume name")
	}
}

func TestScopeRejectsInvalidIdentityInputs(t *testing.T) {
	for _, namespace := range []string{"", "wsl2-short", "WSL2-0123456789abcdef0123456789abcdef", "wsl2-0123456789ABCDEF0123456789ABCDEF"} {
		if _, err := New(hostenv.WSLLayout{StateNamespace: namespace}); err == nil {
			t.Errorf("New(%q) succeeded", namespace)
		}
	}

	scope := testScope(t)
	for _, tc := range []struct {
		group   string
		logical string
	}{
		{"", "cache"},
		{"Node24", "cache"},
		{"node24", ""},
		{"node24", "bad/name"},
	} {
		if _, err := scope.Shared(tc.group, tc.logical); err == nil {
			t.Errorf("Shared(%q, %q) succeeded", tc.group, tc.logical)
		}
	}
	if _, err := scope.Shared(strings.Repeat("a", maxVolumeNameLength), "cache"); err == nil {
		t.Fatal("Shared() accepted an overlong Docker volume name")
	}
	for _, root := range []string{"", "/", "relative", "/home/alice/../bob", "/home/alice/", `/home\alice`, "/home/alice\nproject"} {
		if _, err := scope.Project("node24", "modules", root); err == nil {
			t.Errorf("Project(%q) succeeded", root)
		}
	}
}

func TestScopeExposesExactDockerFilter(t *testing.T) {
	scope := testScope(t)
	if scope.Namespace() != testNamespace || scope.Prefix() != "cb-"+testNamespace+"-" {
		t.Fatalf("Scope identity = (%q, %q)", scope.Namespace(), scope.Prefix())
	}
	if scope.FilterLabel() != NamespaceLabel+"="+testNamespace {
		t.Fatalf("FilterLabel() = %q", scope.FilterLabel())
	}
}

func testScope(t *testing.T) Scope {
	t.Helper()
	scope, err := New(hostenv.WSLLayout{StateNamespace: testNamespace})
	if err != nil {
		t.Fatal(err)
	}
	return scope
}
