package registry_test

import (
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/pathmap"
	"github.com/AviBackToBlack/container-bin/internal/registry"
)

// The registry uses the same logical volume names (node-modules, npm-cache,
// npm-global) for both the node24 and node22 runtimes. The actual Docker
// volume IDs differ because the state_group is part of the name; this test
// proves that two groups with the same logical name cannot collide.
func TestNodeStateGroupsHaveDistinctSharedVolumeIDs(t *testing.T) {
	node24 := pathmap.StatefulSharedVolumeID("node24", "npm-cache")
	node22 := pathmap.StatefulSharedVolumeID("node22", "npm-cache")
	if node24 == node22 {
		t.Fatalf("node24 and node22 shared volume IDs collided: %q", node24)
	}
}

// DefaultTOML now contains both the node24 and node22 runtime families.
// This is a quick smoke check that the package default parses correctly
// from an external test package as well.
func TestDefaultRegistryContainsBothNodeRuntimes(t *testing.T) {
	reg, err := registry.ParseTOML(registry.DefaultTOML)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"node", "npm", "npx", "node22", "npm22", "npx22"} {
		if _, ok := reg.Tools[name]; !ok {
			t.Fatalf("missing default tool %q", name)
		}
	}
}
