package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/policy"
	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/statearchive"
)

func TestStateBackupValidatesItsOwnOutputBeforeSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Docker subprocess fixture uses a POSIX shell; archive validation runs on all platforms")
	}
	stubDir := t.TempDir()
	stub := `#!/bin/sh
case "$1 $2" in
  'image inspect') exit 0 ;;
  'volume inspect') printf 'cb.managed=true\ncb.kind=compat\ncb.owner=python313/venv\n' ;;
  'ps --filter') exit 0 ;;
  'run --rm') cat "$CB_BACKUP_TEST_TAR" ;;
  *) exit 99 ;;
esac
`
	if err := os.WriteFile(filepath.Join(stubDir, "docker"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, tc := range []struct {
		name    string
		header  tar.Header
		wantErr bool
	}{
		{name: "copied_interpreter", header: tar.Header{Name: "./bin/python", Typeflag: tar.TypeReg, Mode: 0755}},
		{name: "legacy_absolute_interpreter", header: tar.Header{Name: "./bin/python", Typeflag: tar.TypeSymlink, Linkname: "/usr/local/bin/python"}, wantErr: true},
		{name: "unrelated_absolute_link", header: tar.Header{Name: "./evil", Typeflag: tar.TypeSymlink, Linkname: "/outside"}, wantErr: true},
		{name: "traversal", header: tar.Header{Name: "../escape", Typeflag: tar.TypeReg}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := filepath.Join(dir, "container-bin.toml")
			if err := os.WriteFile(cfg, []byte(registry.DefaultTOML), 0600); err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			if err := tw.WriteHeader(&tc.header); err != nil {
				t.Fatal(err)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			fixture := filepath.Join(dir, "state.tar")
			if err := os.WriteFile(fixture, buf.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CB_BACKUP_TEST_TAR", fixture)
			output := filepath.Join(dir, "backup.zip")
			err := Backup(cfg, []string{output, "--state", "cb-python-313-global"}, "test", policy.Policy{})
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "state backup is not restorable") {
					t.Fatalf("error = %v, want producer validation failure", err)
				}
				if _, err := os.Stat(output); !os.IsNotExist(err) {
					t.Fatalf("unsuccessful backup left an archive: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			zr, err := zip.OpenReader(output)
			if err != nil {
				t.Fatal(err)
			}
			defer zr.Close()
			archive, err := statearchive.Open(zr.File)
			if err != nil {
				t.Fatalf("successful backup rejected by consumer: %v", err)
			}
			if got := archive.Manifest.Volumes[0].Labels["cb.owner"]; got != "python313/venv" {
				t.Fatalf("backup rewrote existing identity: %q", got)
			}
		})
	}
}
