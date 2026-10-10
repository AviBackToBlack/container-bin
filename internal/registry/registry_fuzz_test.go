package registry

import (
	"reflect"
	"testing"
)

func FuzzParseTOML(f *testing.F) {
	for _, seed := range []string{
		DefaultTOML,
		"",
		"[tools.demo]\nimage = \"example/demo:1\"\n",
		"[tools.demo]\nimage = \"example/demo:1\"\n[tools.demo]\n",
		"[tools.con]\nimage = \"example/demo:1\"\n",
		"[tools.demo]\nimage = \"example/demo:1\"\nproject_markers = [\"../escape\"]\n",
		"[tools.demo]\nimage = \"unterminated\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		reg, err := ParseTOML(input)
		again, againErr := ParseTOML(input)
		if (err == nil) != (againErr == nil) {
			t.Fatal("parsing the same registry changed acceptance")
		}
		if err != nil {
			return
		}
		if !reflect.DeepEqual(reg, again) {
			t.Fatal("accepted registry parsing is nondeterministic")
		}
		for name, tool := range reg.Tools {
			if !ValidToolName(name) || ReservedToolName(name) || tool.Name != name || tool.Image == "" {
				t.Fatalf("accepted unsafe/incomplete tool identity: %q", name)
			}
			for _, marker := range tool.ProjectMarkers {
				if err := ValidateProjectMarker(marker); err != nil {
					t.Fatalf("accepted unsafe project marker %q: %v", marker, err)
				}
			}
		}
	})
}
