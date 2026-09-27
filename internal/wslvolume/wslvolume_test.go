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
	if volume.Name != "cb-"+testNamespace+"-node24-npm-cache" {
		t.Fatalf("Shared() name = %q", volume.Name)
	}
	wantLabels := map[string]string{
		"cb.managed": "true", "cb.kind": "shared", "cb.owner": "node24/npm-cache",
		NamespaceLabel: testNamespace,
	}
	if !reflect.DeepEqual(volume.Labels, wantLabels) {
		t.Fatalf("Shared() labels = %#v, want %#v", volume.Labels, wantLabels)
	}
	if !scope.Owns(volume.Name, volume.Labels) {
		t.Fatal("Scope does not recognize its shared volume")
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
	if lower.Name == upper.Name || lower.Labels["cb.project_hash"] == upper.Labels["cb.project_hash"] {
		t.Fatal("case-distinct Linux project roots share volume identity")
	}
	if lower.Labels["cb.project_path"] != "/home/alice/project" || !strings.HasSuffix(lower.Name, "-"+lower.Labels["cb.project_hash"]) {
		t.Fatalf("Project() = %#v", lower)
	}
	if lower.Labels["cb.project_hash"] != "e9b06e6a6ea9" {
		t.Fatalf("stable project hash = %q", lower.Labels["cb.project_hash"])
	}
	if !scope.Owns(lower.Name, lower.Labels) {
		t.Fatal("Scope does not recognize its project volume")
	}
}

func TestScopeRequiresNameAndLabelNamespaceProof(t *testing.T) {
	scope := testScope(t)
	volume, err := scope.Shared("go124", "gomodcache")
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Volume){
		"wrong prefix": func(v *Volume) { v.Name = "cb-other-go124-gomodcache" },
		"prefix only":  func(v *Volume) { v.Name = scope.Prefix() },
		"unmanaged":    func(v *Volume) { delete(v.Labels, "cb.managed") },
		"wrong label":  func(v *Volume) { v.Labels[NamespaceLabel] = "wsl2-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := Volume{Name: volume.Name, Labels: cloneLabels(volume.Labels)}
			mutate(&candidate)
			if scope.Owns(candidate.Name, candidate.Labels) {
				t.Fatal("Scope accepted incomplete ownership proof")
			}
		})
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
	if second.Owns(volume.Name, volume.Labels) {
		t.Fatal("another WSL scope adopted the first scope's project volume")
	}
	other, err := second.Project("node24", "node-modules", "/home/alice/project")
	if err != nil {
		t.Fatal(err)
	}
	if volume.Name == other.Name {
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

func cloneLabels(labels map[string]string) map[string]string {
	clone := make(map[string]string, len(labels))
	for key, value := range labels {
		clone[key] = value
	}
	return clone
}
