package selfupdate

import (
	"strings"
	"testing"
)

func FuzzChecksumManifest(f *testing.F) {
	digest := strings.Repeat("a", 64)
	amd64 := "container-bin-v2.0.0-windows-amd64.zip"
	arm64 := "container-bin-v2.0.0-windows-arm64.zip"
	legacy := digest + "  cb.exe\n" + strings.Repeat("b", 64) + "  " + amd64 + "\n"
	dual := legacy + strings.Repeat("c", 64) + "  " + arm64 + "\n"
	for _, seed := range []string{
		"", legacy, dual,
		strings.TrimSuffix(dual, "\n"),
		strings.ReplaceAll(dual, "\n", "\r\n"),
		dual + digest + "  cb.exe\n",
		strings.Replace(dual, digest, strings.Repeat("d", 64), 1),
		strings.Replace(dual, amd64, "../"+amd64, 1),
		strings.ToUpper(dual),
	} {
		f.Add(seed, false)
		f.Add(seed, true)
	}
	f.Fuzz(func(t *testing.T, input string, dualArch bool) {
		layout := checksumLayoutLegacyAMD64
		names := []string{"cb.exe", amd64}
		if dualArch {
			layout = checksumLayoutDualArch
			names = append(names, arm64)
		}
		plan := Plan{Target: "v2.0.0", Binary: Asset{Name: "cb.exe"}, checksumLayout: layout}
		accepted := verifyChecksumManifest([]byte(input), plan, digest) == nil
		// Independent format oracle: exact asset set, canonical lowercase hashes,
		// LF lines, two-space separators, and the pinned binary digest.
		want := canonicalFuzzManifest(input, names, digest)
		if accepted != want {
			t.Fatalf("checksum acceptance = %v, independent oracle = %v for %q", accepted, want, input)
		}
	})
}

func canonicalFuzzManifest(input string, names []string, binaryDigest string) bool {
	if !strings.HasSuffix(input, "\n") || strings.ContainsRune(input, '\r') {
		return false
	}
	lines := strings.Split(strings.TrimSuffix(input, "\n"), "\n")
	if len(lines) != len(names) {
		return false
	}
	remaining := make(map[string]bool, len(names))
	for _, name := range names {
		remaining[name] = true
	}
	for _, line := range lines {
		if len(line) < 67 || line[64:66] != "  " {
			return false
		}
		for _, char := range line[:64] {
			if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
				return false
			}
		}
		name := line[66:]
		if !remaining[name] || name == "cb.exe" && line[:64] != binaryDigest {
			return false
		}
		delete(remaining, name)
	}
	return len(remaining) == 0
}
