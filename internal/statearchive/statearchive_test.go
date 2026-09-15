package statearchive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AviBackToBlack/container-bin/internal/pathmap"
)

func TestOpenValidatesManifestArchiveAndTar(t *testing.T) {
	tarData := testTar(t, &tar.Header{Name: "./cache.txt", Mode: 0600, Size: 5, Typeflag: tar.TypeReg}, []byte("hello"))
	manifest := testManifest(tarData)
	archive := testZip(t, manifest, map[string][]byte{manifest.Volumes[0].Archive: tarData})
	opened, err := Open(archive.File)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.Manifest.Volumes) != 1 || opened.Manifest.Volumes[0].Name != "cb-demo-cache" {
		t.Fatalf("unexpected manifest: %+v", opened.Manifest)
	}
}

func TestOpenRejectsTamperingAndUnsafeTarEntries(t *testing.T) {
	validTar := testTar(t, &tar.Header{Name: "./cache.txt", Mode: 0600, Size: 5, Typeflag: tar.TypeReg}, []byte("hello"))
	tests := []struct {
		name    string
		tarData []byte
		mutate  func(*Manifest)
		extra   map[string][]byte
		want    string
	}{
		{
			name:    "checksum",
			tarData: validTar,
			mutate:  func(m *Manifest) { m.Volumes[0].SHA256 = strings.Repeat("0", 64) },
			want:    "checksum/size",
		},
		{
			name:    "traversal",
			tarData: testTar(t, &tar.Header{Name: "../escape", Mode: 0600, Size: 1, Typeflag: tar.TypeReg}, []byte("x")),
			want:    "escapes the volume root",
		},
		{
			name:    "device",
			tarData: testTar(t, &tar.Header{Name: "./device", Mode: 0600, Typeflag: tar.TypeChar}, nil),
			want:    "unsupported type",
		},
		{
			name:    "hardlink_traversal",
			tarData: testTar(t, &tar.Header{Name: "./hardlink", Mode: 0600, Typeflag: tar.TypeLink, Linkname: "../outside"}, nil),
			want:    "target \"../outside\" escapes the volume root",
		},
		{
			name:    "symlink_absolute",
			tarData: testTar(t, &tar.Header{Name: "./symlink", Mode: 0777, Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, nil),
			want:    "target \"/etc/passwd\" escapes the volume root",
		},
		{
			name:    "unreferenced_archive",
			tarData: validTar,
			extra:   map[string][]byte{"state/volumes/cb-other.tar": validTar},
			want:    "unreferenced state archive",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			manifest := testManifest(tc.tarData)
			if tc.mutate != nil {
				tc.mutate(&manifest)
			}
			entries := map[string][]byte{manifest.Volumes[0].Archive: tc.tarData}
			for name, data := range tc.extra {
				entries[name] = data
			}
			archive := testZip(t, manifest, entries)
			_, err := Open(archive.File)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestValidateTarHeaderLinkTargets(t *testing.T) {
	tests := []struct {
		name    string
		header  tar.Header
		wantErr bool
	}{
		{name: "hardlink_inside_root", header: tar.Header{Name: "./dir/hard", Typeflag: tar.TypeLink, Linkname: "./dir/target"}},
		{name: "symlink_parent_inside_root", header: tar.Header{Name: "./dir/link", Typeflag: tar.TypeSymlink, Linkname: "../target"}},
		{name: "symlink_parent_escape", header: tar.Header{Name: "./dir/link", Typeflag: tar.TypeSymlink, Linkname: "../../outside"}, wantErr: true},
		{name: "hardlink_absolute", header: tar.Header{Name: "./hard", Typeflag: tar.TypeLink, Linkname: "/volume/target"}, wantErr: true},
		{name: "empty_target", header: tar.Header{Name: "./link", Typeflag: tar.TypeSymlink}, wantErr: true},
		{name: "nul_target", header: tar.Header{Name: "./link", Typeflag: tar.TypeSymlink, Linkname: "target\x00suffix"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTarHeader(&tt.header)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateManagedIdentity(t *testing.T) {
	project := t.TempDir()
	projectName := pathmap.StatefulProjectVolumeID("node24", "node-modules", project, true)
	path, hash, err := validateManagedIdentity(projectName, map[string]string{
		"cb.managed":      "true",
		"cb.kind":         "project",
		"cb.owner":        "node24/node-modules",
		"cb.project_path": project,
		"cb.project_hash": pathmap.VolumeHash(project),
	})
	if err != nil || path != project || hash != pathmap.VolumeHash(project) {
		t.Fatalf("project identity = %q %q, err=%v", path, hash, err)
	}
	if _, _, err := validateManagedIdentity("cb-node24-npm-cache", map[string]string{
		"cb.managed": "true", "cb.kind": "shared", "cb.owner": "node24/npm-cache",
	}); err != nil {
		t.Fatal(err)
	}
	bad := map[string]string{
		"cb.managed":      "true",
		"cb.kind":         "project",
		"cb.owner":        "node24/node-modules",
		"cb.project_path": project,
		"cb.project_hash": "wrong",
	}
	if _, _, err := validateManagedIdentity(projectName, bad); err == nil || !strings.Contains(err.Error(), "inconsistent cb.project_hash") {
		t.Fatalf("unexpected bad identity error: %v", err)
	}
}

func TestHelperCommandAttachesOnlyRestoreStdin(t *testing.T) {
	backup := helperCommand("cb-demo-cache", true, false, "tar")
	restore := helperCommand("cb-demo-cache", false, true, "tar")
	for _, want := range []string{"--pull never", "--network none", "--read-only", HelperImage} {
		if !strings.Contains(strings.Join(backup.Args, " "), want) {
			t.Fatalf("backup helper args missing %q: %#v", want, backup.Args)
		}
	}
	if strings.Contains(" "+strings.Join(backup.Args, " ")+" ", " -i ") {
		t.Fatalf("backup unexpectedly attaches stdin: %#v", backup.Args)
	}
	if !strings.Contains(" "+strings.Join(restore.Args, " ")+" ", " -i ") {
		t.Fatalf("restore does not attach stdin: %#v", restore.Args)
	}
}

func testManifest(tarData []byte) Manifest {
	sum := sha256.Sum256(tarData)
	return Manifest{
		SchemaVersion: 1,
		CreatedAt:     time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
		CBVersion:     "test",
		HelperImage:   HelperImage,
		Volumes: []Volume{{
			Name:        "cb-demo-cache",
			Archive:     "state/volumes/cb-demo-cache.tar",
			ArchiveSize: int64(len(tarData)),
			SHA256:      hex.EncodeToString(sum[:]),
			Labels: map[string]string{
				"cb.managed": "true",
				"cb.kind":    "shared",
				"cb.owner":   "demo/cache",
			},
		}},
	}
}

func testTar(t *testing.T, header *tar.Header, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if len(data) > 0 {
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testZip(t *testing.T, manifest Manifest, entries map[string][]byte) *zip.Reader {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	w, err := zw.Create(ManifestName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(manifestData); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	return reader
}
