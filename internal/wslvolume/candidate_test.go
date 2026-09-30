package wslvolume

import (
	"reflect"
	"strings"
	"testing"
)

func TestProveCandidateReconstructsExactSharedAndProjectVolumes(t *testing.T) {
	scope := testScope(t)
	shared, err := scope.Shared("go124", "gomodcache")
	if err != nil {
		t.Fatal(err)
	}
	project, err := scope.Project("node24", "node-modules", "/home/alice/Project")
	if err != nil {
		t.Fatal(err)
	}
	for name, expected := range map[string]Volume{"shared": shared, "project": project} {
		t.Run(name, func(t *testing.T) {
			proved, err := ProveCandidate(scope, candidateFromVolume(expected))
			if err != nil {
				t.Fatal(err)
			}
			if proved.Name() != expected.Name() || !reflect.DeepEqual(proved.Labels(), expected.Labels()) {
				t.Fatalf("proved volume = %s %#v, want %s %#v", proved.Name(), proved.Labels(), expected.Name(), expected.Labels())
			}
		})
	}
}

func TestProveCandidateRejectsForeignOrIncompleteIdentity(t *testing.T) {
	scope := testScope(t)
	shared, err := scope.Shared("go124", "gomodcache")
	if err != nil {
		t.Fatal(err)
	}
	project, err := scope.Project("node24", "node-modules", "/home/alice/project")
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]struct {
		candidate Candidate
		want      string
	}{
		"zero candidate": {want: "incomplete identity"},
		"foreign driver": {
			candidate: mutateCandidate(candidateFromVolume(shared), func(candidate *Candidate) { candidate.driver = "plugin" }),
			want:      "local driver and scope",
		},
		"foreign scope": {
			candidate: mutateCandidate(candidateFromVolume(shared), func(candidate *Candidate) { candidate.scope = "global" }),
			want:      "local driver and scope",
		},
		"wrong namespace": {
			candidate: mutateCandidate(candidateFromVolume(shared), func(candidate *Candidate) {
				candidate.labels[NamespaceLabel] = "wsl2-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			}),
			want: "out-of-scope",
		},
		"missing managed label": {
			candidate: mutateCandidate(candidateFromVolume(shared), func(candidate *Candidate) { delete(candidate.labels, "cb.managed") }),
			want:      "exact constructed",
		},
		"extra label": {
			candidate: mutateCandidate(candidateFromVolume(shared), func(candidate *Candidate) { candidate.labels["extra"] = "value" }),
			want:      "exact constructed",
		},
		"malformed owner": {
			candidate: mutateCandidate(candidateFromVolume(shared), func(candidate *Candidate) { candidate.labels["cb.owner"] = "go124/cache/extra" }),
			want:      "invalid owner",
		},
		"unsupported kind": {
			candidate: mutateCandidate(candidateFromVolume(shared), func(candidate *Candidate) { candidate.labels["cb.kind"] = "compat" }),
			want:      "unsupported kind",
		},
		"wrong project hash": {
			candidate: mutateCandidate(candidateFromVolume(project), func(candidate *Candidate) { candidate.labels["cb.project_hash"] = "000000000000" }),
			want:      "exact constructed",
		},
		"invalid project path": {
			candidate: mutateCandidate(candidateFromVolume(project), func(candidate *Candidate) { candidate.labels["cb.project_path"] = "relative" }),
			want:      "canonical absolute",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ProveCandidate(scope, test.candidate); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ProveCandidate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestProveCandidateRejectsInvalidScopeAndNameEncoding(t *testing.T) {
	shared, err := testScope(t).Shared("go124", "gomodcache")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ProveCandidate(Scope{}, candidateFromVolume(shared)); err == nil {
		t.Fatal("ProveCandidate() accepted zero scope")
	}
	candidate := candidateFromVolume(shared)
	candidate.name = testScope(t).Prefix() + "4-go124-10-gomodcache"
	if _, err := ProveCandidate(testScope(t), candidate); err == nil || !strings.Contains(err.Error(), "exact constructed") {
		t.Fatalf("invalid name-encoding error = %v", err)
	}
}

func candidateFromVolume(volume Volume) Candidate {
	return Candidate{name: volume.Name(), driver: "local", scope: "local", labels: volume.Labels()}
}

func mutateCandidate(candidate Candidate, mutate func(*Candidate)) Candidate {
	mutate(&candidate)
	return candidate
}
