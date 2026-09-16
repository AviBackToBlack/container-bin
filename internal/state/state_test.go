package state

import (
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/registry"
)

func TestOrphanFilterResolvesAliasesAndConcreteToolsToStateGroup(t *testing.T) {
	reg := registry.Default()
	cases := []struct {
		name      string
		filter    string
		owner     string
		wantMatch bool
		wantGroup string
	}{
		{name: "empty", owner: "node24/node-modules", wantMatch: true},
		{name: "default alias", filter: "node", owner: "node24/node-modules", wantMatch: true, wantGroup: "node24"},
		{name: "concrete sibling", filter: "npm24", owner: "node24/node-modules", wantMatch: true, wantGroup: "node24"},
		{name: "state group", filter: "node24", owner: "node24/node-modules", wantMatch: true, wantGroup: "node24"},
		{name: "exact owner", filter: "node24/node-modules", owner: "node24/node-modules", wantMatch: true},
		{name: "other version", filter: "node", owner: "node22/node-modules", wantMatch: false, wantGroup: "node24"},
		{name: "legacy python", filter: "python", owner: "python313/venv", wantMatch: true, wantGroup: "python313"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, group := resolveGCFilter(reg, tc.filter)
			if group != tc.wantGroup {
				t.Fatalf("resolved state group = %q, want %q", group, tc.wantGroup)
			}
			if got := orphanOwnerMatchesFilter(tc.owner, tc.filter, group); got != tc.wantMatch {
				t.Fatalf("orphanOwnerMatchesFilter(%q, %q, %q) = %v, want %v", tc.owner, tc.filter, group, got, tc.wantMatch)
			}
		})
	}
}
