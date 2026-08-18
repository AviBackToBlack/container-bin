package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestPathNormalizationCorpus is the RM-6b table-driven corpus. It encodes the
// RM-6a classification (docs/windows-paths.md) as executable assertions across
// combinations of Windows path-argument syntax that the individual P-row
// tests in pathforms_test.go and the API tests in pathmap_test.go do not
// exercise together: drive-letter absolutes, forward- and backslash forms,
// "." / "..", spaces, commas, non-ASCII characters, mixed case, and trailing
// dots -- all run end to end through mapToolArgs. Reparse-point scenarios
// (P1/P2) are cited, not repeated here: they already require a real on-disk
// symlink and are proven in pathforms_test.go.
func TestPathNormalizationCorpus(t *testing.T) {
	root, ext := windowsFixtures(t)

	mustWriteFile(t, filepath.Join(root, "sub", "deep", "e.txt"), []byte{})
	mustWriteFile(t, filepath.Join(root, "sub dir with spaces", "a.txt"), []byte{})
	mustWriteFile(t, filepath.Join(root, "comma,dir", "commafile.txt"), []byte{})
	mustWriteFile(t, filepath.Join(root, "café", "nonascii.txt"), []byte{})
	mustWriteFile(t, filepath.Join(root, "MixedCaseDir", "D.TXT"), []byte{})
	mustWriteFile(t, filepath.Join(root, "trailingdot"), []byte{})

	extFile := filepath.Join(ext, "b.txt")
	dotdotRel, err := filepath.Rel(root, extFile)
	if err != nil {
		t.Fatalf("filepath.Rel(%q, %q): %v", root, extFile, err)
	}
	if !strings.HasPrefix(dotdotRel, `..\`) {
		t.Fatalf("test setup: dotdotRel %q does not start with ..\\; fixture layout changed", dotdotRel)
	}

	// wantUnder derives the expected mapped path from the relative path as
	// created on disk, lowercasing and slash-converting exactly as
	// canonicalPath/mapArg do -- so a case or slash mistake in this table
	// cannot silently produce a false pass.
	wantUnder := func(rel string) string {
		return "/workspace/" + strings.ToLower(filepath.ToSlash(rel))
	}

	cases := []struct {
		name          string
		arg           string
		want          string
		wantUnchanged bool
		wantExternal  bool // asserted against mounts[0] == ext, target /cb/mounts/0
	}{
		// Drive-letter absolute, typed lowercase -- baseline anchor for the
		// corpus; general absolute-path rule.
		{
			name: "drive_letter_absolute_baseline",
			arg:  strings.ToLower(filepath.Join(root, "sub", "deep", "e.txt")),
			want: wantUnder("sub/deep/e.txt"),
		},
		// Forward-slash absolute form -- isWindowsAbsPath accepts '/' as the
		// third character, and canonicalPath/GetFullPathNameW normalize it.
		{
			name: "forward_slash_absolute",
			arg:  strings.ReplaceAll(filepath.Join(root, "sub", "deep", "e.txt"), `\`, "/"),
			want: wantUnder("sub/deep/e.txt"),
		},
		// Explicit relative, backslash form.
		{
			name: "explicit_relative_backslash",
			arg:  `.\sub\deep\e.txt`,
			want: wantUnder("sub/deep/e.txt"),
		},
		// Explicit relative, forward-slash form.
		{
			name: "explicit_relative_forward_slash",
			arg:  "./sub/deep/e.txt",
			want: wantUnder("sub/deep/e.txt"),
		},
		// ".." relative argument that legitimately escapes the project root:
		// it must land as an external mount, the same as an absolute external
		// argument (TestMapToolArgsExternalAbsolute in pathmap_test.go), not
		// be silently declined or folded into the workspace.
		{
			name:         "dotdot_relative_escapes_to_external_mount",
			arg:          dotdotRel,
			wantExternal: true,
		},
		// Spaces in a path component (issue-named shape).
		{
			name: "spaces_in_component",
			arg:  filepath.Join(root, "sub dir with spaces", "a.txt"),
			want: wantUnder("sub dir with spaces/a.txt"),
		},
		// Comma in a path component (issue-named shape). Note: this is a
		// different rule from the mountSpec dst comma rejection added in
		// RM-1a -- that check guards a constructed container-side mount
		// destination string, not an ordinary in-root argument like this one.
		{
			name: "comma_in_component",
			arg:  filepath.Join(root, "comma,dir", "commafile.txt"),
			want: wantUnder("comma,dir/commafile.txt"),
		},
		// Non-ASCII path component (issue-named shape).
		{
			name: "non_ascii_component",
			arg:  filepath.Join(root, "café", "nonascii.txt"),
			want: wantUnder("café/nonascii.txt"),
		},
		// Mixed-case drive/prefix typed against a mixed-case real component
		// (P5): Windows' case-insensitive lookup finds the file regardless of
		// typed case, and the mapped result is always lowercase.
		{
			name: "mixed_case_component_and_prefix",
			arg:  filepath.Join(strings.ToUpper(root), "mixedcasedir", "d.txt"),
			want: wantUnder("MixedCaseDir/D.TXT"),
		},
		// Trailing dot combined with forward slashes (P11): GetFullPathNameW
		// strips the trailing dot regardless of which separator was used.
		{
			name: "trailing_dot_with_forward_slashes",
			arg:  strings.ReplaceAll(filepath.Join(root, "trailingdot."), `\`, "/"),
			want: wantUnder("trailingdot"),
		},
		// Forward-slash UNC-shaped argument (P7 contrast): unlike the
		// backslash form, this never reaches the existing-relative filesystem
		// probe at all -- resolveWindowsPathArgMode's fourth branch only
		// triggers on an argument containing a backslash, and this one has
		// none (see the P7 rationale in docs/windows-paths.md). Declined by
		// shape, unconditionally.
		{
			name:          "forward_slash_unc_declined_unconditionally",
			arg:           "//server/share/x",
			wantUnchanged: true,
		},
	}

	tool := Tool{Name: "demo"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mapped, mounts, err := mapToolArgs(tool, root, root, "/workspace", []string{tc.arg})
			if err != nil {
				t.Fatalf("%s: err=%v", tc.name, err)
			}
			if len(mapped) != 1 {
				t.Fatalf("%s: mapped=%#v, want 1 element", tc.name, mapped)
			}
			switch {
			case tc.wantExternal:
				if len(mounts) != 1 || mounts[0].Source != ext || mounts[0].Target != "/cb/mounts/0" {
					t.Fatalf("%s: mounts=%#v", tc.name, mounts)
				}
				if want := mounts[0].Target + "/b.txt"; mapped[0] != want {
					t.Fatalf("%s: mapped=%q, want %q", tc.name, mapped[0], want)
				}
			case tc.wantUnchanged:
				if mapped[0] != tc.arg {
					t.Fatalf("%s: mapped=%q, want unchanged %q", tc.name, mapped[0], tc.arg)
				}
				if len(mounts) != 0 {
					t.Fatalf("%s: expected no mounts, got %#v", tc.name, mounts)
				}
			default:
				if mapped[0] != tc.want {
					t.Fatalf("%s: mapped=%q, want %q", tc.name, mapped[0], tc.want)
				}
				if len(mounts) != 0 {
					t.Fatalf("%s: expected no mounts, got %#v", tc.name, mounts)
				}
			}
		})
	}
}
