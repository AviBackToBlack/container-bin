package pathmap

import (
	"reflect"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/registry"
)

func FuzzNormalizeWindowsToolArgs(f *testing.F) {
	for _, seed := range []string{"", `C:\work\file with spaces`, `\\server\share\file`, "../escape", "--", "--file=", "\x00\xff"} {
		f.Add(seed, "--file=")
	}
	f.Fuzz(func(t *testing.T, value, literal string) {
		tool := registry.Tool{Name: "demo", PathEquals: []string{"--file"}}
		args := []string{"before", "--file=", value, "--", "--file=", literal}
		before := append([]string(nil), args...)
		want := []string{"before", "--file=" + value, "--", "--file=", literal}
		got := normalizeToolArgsForHost("windows", tool, args)
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(args, before) {
			t.Fatalf("Windows option normalization changed argv boundaries: %q", got)
		}
		for _, host := range []string{"linux", "darwin"} {
			if got := normalizeToolArgsForHost(host, tool, args); !reflect.DeepEqual(got, before) {
				t.Fatalf("%s argv unexpectedly normalized: %q", host, got)
			}
		}
	})
}
