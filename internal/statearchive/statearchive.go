// Package statearchive creates and restores checksummed archives of explicitly
// selected ContainerBin-managed Docker volumes.
package statearchive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"reflect"
	"sort"
	"strings"
	"time"

	"os/exec"

	"github.com/AviBackToBlack/container-bin/internal/dockervol"
	"github.com/AviBackToBlack/container-bin/internal/pathmap"
)

const (
	ManifestName = "state/manifest.json"
	// Alpine 3.23.5 official multi-architecture index. State operations never
	// pull implicitly and run this immutable helper with no network and a
	// read-only container root.
	HelperImage = "docker.io/library/alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40"
	maxManifest = 1 << 20
)

type Manifest struct {
	SchemaVersion int      `json:"schema_version"`
	CreatedAt     string   `json:"created_at"`
	CBVersion     string   `json:"cb_version"`
	HelperImage   string   `json:"helper_image"`
	Volumes       []Volume `json:"volumes"`
}

type Volume struct {
	Name        string            `json:"name"`
	Archive     string            `json:"archive"`
	ArchiveSize int64             `json:"archive_size"`
	SHA256      string            `json:"sha256"`
	Labels      map[string]string `json:"labels"`
	ProjectPath string            `json:"project_path,omitempty"`
	ProjectHash string            `json:"project_hash,omitempty"`
}

type Plan struct {
	Name   string
	Status string
}

type Archive struct {
	Manifest Manifest
	files    map[string]*zip.File
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.n += int64(n)
	return n, err
}

func Backup(zw *zip.Writer, names []string, cbVersion string, created time.Time) (Manifest, error) {
	if len(names) == 0 {
		return Manifest{}, errors.New("state backup requires at least one explicitly named volume")
	}
	if err := ensureHelperImage(); err != nil {
		return Manifest{}, err
	}
	names = append([]string(nil), names...)
	sort.Strings(names)
	seen := map[string]bool{}
	manifest := Manifest{
		SchemaVersion: 1,
		CreatedAt:     created.UTC().Format(time.RFC3339),
		CBVersion:     cbVersion,
		HelperImage:   HelperImage,
	}
	for _, name := range names {
		if !validVolumeName(name) {
			return Manifest{}, fmt.Errorf("invalid Docker volume name %q", name)
		}
		if seen[name] {
			return Manifest{}, fmt.Errorf("volume %q was selected more than once", name)
		}
		seen[name] = true
		labels, err := dockervol.Labels(name)
		if err != nil {
			return Manifest{}, err
		}
		projectPath, projectHash, err := validateManagedIdentity(name, labels)
		if err != nil {
			return Manifest{}, err
		}
		if err := requireQuiescent(name); err != nil {
			return Manifest{}, err
		}
		fmt.Printf("archiving state: %s\n", name)
		entry := Volume{
			Name:        name,
			Archive:     volumeArchiveName(name),
			Labels:      labels,
			ProjectPath: projectPath,
			ProjectHash: projectHash,
		}
		h := sha256.New()
		header := &zip.FileHeader{Name: entry.Archive, Method: zip.Deflate}
		header.SetModTime(created)
		writer, err := zw.CreateHeader(header)
		if err != nil {
			return Manifest{}, err
		}
		counter := &countingWriter{w: io.MultiWriter(writer, h)}
		if err := exportVolume(name, counter); err != nil {
			return Manifest{}, err
		}
		entry.ArchiveSize = counter.n
		entry.SHA256 = hex.EncodeToString(h.Sum(nil))
		manifest.Volumes = append(manifest.Volumes, entry)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Manifest{}, err
	}
	data = append(data, '\n')
	header := &zip.FileHeader{Name: ManifestName, Method: zip.Deflate}
	header.SetModTime(created)
	w, err := zw.CreateHeader(header)
	if err != nil {
		return Manifest{}, err
	}
	if _, err := w.Write(data); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func Open(files []*zip.File) (*Archive, error) {
	byName := map[string]*zip.File{}
	for _, f := range files {
		if _, duplicate := byName[f.Name]; duplicate {
			return nil, fmt.Errorf("backup contains duplicate entry %q", f.Name)
		}
		byName[f.Name] = f
	}
	mf, ok := byName[ManifestName]
	if !ok {
		return nil, errors.New("backup does not contain a state manifest")
	}
	r, err := mf.Open()
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(io.LimitReader(r, maxManifest+1))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	err = decoder.Decode(&manifest)
	if err == nil {
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			err = errors.New("state manifest contains trailing data")
		}
	}
	r.Close()
	if err != nil {
		return nil, fmt.Errorf("invalid state manifest: %w", err)
	}
	if mf.UncompressedSize64 > maxManifest {
		return nil, errors.New("state manifest exceeds 1 MiB")
	}
	if manifest.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported state manifest schema_version %d", manifest.SchemaVersion)
	}
	if manifest.HelperImage == "" || manifest.CreatedAt == "" || manifest.CBVersion == "" {
		return nil, errors.New("state manifest is missing required metadata")
	}
	if _, err := time.Parse(time.RFC3339, manifest.CreatedAt); err != nil {
		return nil, fmt.Errorf("state manifest created_at: %w", err)
	}
	if len(manifest.Volumes) == 0 {
		return nil, errors.New("state manifest contains no volumes")
	}
	seenVolumes, referenced := map[string]bool{}, map[string]bool{}
	for i := range manifest.Volumes {
		v := &manifest.Volumes[i]
		if !validVolumeName(v.Name) || seenVolumes[v.Name] {
			return nil, fmt.Errorf("invalid or duplicate state volume name %q", v.Name)
		}
		seenVolumes[v.Name] = true
		if v.Archive != volumeArchiveName(v.Name) {
			return nil, fmt.Errorf("volume %q has unexpected archive path %q", v.Name, v.Archive)
		}
		if referenced[v.Archive] {
			return nil, fmt.Errorf("state archive %q is referenced more than once", v.Archive)
		}
		referenced[v.Archive] = true
		zf, ok := byName[v.Archive]
		if !ok {
			return nil, fmt.Errorf("backup is missing %s", v.Archive)
		}
		if v.ArchiveSize <= 0 || uint64(v.ArchiveSize) != zf.UncompressedSize64 {
			return nil, fmt.Errorf("volume %q archive size does not match the manifest", v.Name)
		}
		if len(v.SHA256) != 64 {
			return nil, fmt.Errorf("volume %q has invalid SHA-256", v.Name)
		}
		if _, err := hex.DecodeString(v.SHA256); err != nil {
			return nil, fmt.Errorf("volume %q has invalid SHA-256", v.Name)
		}
		projectPath, projectHash, err := validateManagedIdentity(v.Name, v.Labels)
		if err != nil {
			return nil, err
		}
		if projectPath != v.ProjectPath || projectHash != v.ProjectHash {
			return nil, fmt.Errorf("volume %q project identity does not match its labels", v.Name)
		}
		if err := validateVolumeTar(zf, v); err != nil {
			return nil, err
		}
	}
	for name := range byName {
		if strings.HasPrefix(name, "state/volumes/") && !referenced[name] {
			return nil, fmt.Errorf("backup contains unreferenced state archive %q", name)
		}
	}
	return &Archive{Manifest: manifest, files: byName}, nil
}

func (a *Archive) Plan() ([]Plan, error) {
	if err := ensureHelperImage(); err != nil {
		return nil, err
	}
	existing, err := dockervol.ExistsSet()
	if err != nil {
		return nil, err
	}
	plans := make([]Plan, 0, len(a.Manifest.Volumes))
	for _, v := range a.Manifest.Volumes {
		if err := requireQuiescent(v.Name); err != nil {
			return nil, err
		}
		status := "CREATE"
		if existing[v.Name] {
			labels, err := dockervol.Labels(v.Name)
			if err != nil {
				return nil, err
			}
			if !reflect.DeepEqual(labels, v.Labels) {
				return nil, fmt.Errorf("volume %q exists with labels that do not match the backup", v.Name)
			}
			empty, err := volumeEmpty(v.Name)
			if err != nil {
				return nil, err
			}
			if !empty {
				return nil, fmt.Errorf("volume %q already exists and is not empty", v.Name)
			}
			status = "RESTORE_EMPTY"
		}
		plans = append(plans, Plan{Name: v.Name, Status: status})
	}
	return plans, nil
}

func (a *Archive) Restore() error {
	if _, err := a.Plan(); err != nil {
		return err
	}
	for index, v := range a.Manifest.Volumes {
		if err := requireQuiescent(v.Name); err != nil {
			return fmt.Errorf("restored %d volume(s) before failure: %w", index, err)
		}
		if err := dockervol.EnsureManaged(v.Name, v.Labels); err != nil {
			return fmt.Errorf("restored %d volume(s) before failure: %w", index, err)
		}
		labels, err := dockervol.Labels(v.Name)
		if err != nil || !reflect.DeepEqual(labels, v.Labels) {
			if err == nil {
				err = errors.New("labels changed between restore planning and apply")
			}
			return fmt.Errorf("restored %d volume(s) before failure at %q: %w", index, v.Name, err)
		}
		empty, err := volumeEmpty(v.Name)
		if err != nil || !empty {
			if err == nil {
				err = errors.New("destination became non-empty between restore planning and apply")
			}
			return fmt.Errorf("restored %d volume(s) before failure at %q: %w", index, v.Name, err)
		}
		r, err := a.files[v.Archive].Open()
		if err != nil {
			return fmt.Errorf("restored %d volume(s) before failure: %w", index, err)
		}
		fmt.Printf("restoring state: %s\n", v.Name)
		err = importVolume(v.Name, r)
		r.Close()
		if err != nil {
			return fmt.Errorf("restored %d volume(s) before failure at %q; destination may now be partial: %w", index, v.Name, err)
		}
	}
	return nil
}

func volumeArchiveName(name string) string {
	return "state/volumes/" + name + ".tar"
}

func validVolumeName(name string) bool {
	if name == "" || !((name[0] >= 'a' && name[0] <= 'z') || (name[0] >= 'A' && name[0] <= 'Z') || (name[0] >= '0' && name[0] <= '9')) {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-' {
			continue
		}
		return false
	}
	return strings.HasPrefix(strings.ToLower(name), "cb-")
}

func validateManagedIdentity(name string, labels map[string]string) (string, string, error) {
	if labels["cb.managed"] != "true" {
		return "", "", fmt.Errorf("volume %q is not labeled cb.managed=true", name)
	}
	kind, owner := labels["cb.kind"], labels["cb.owner"]
	if owner == "" {
		return "", "", fmt.Errorf("volume %q has no cb.owner label", name)
	}
	switch kind {
	case "shared":
		if labels["cb.project_path"] != "" || labels["cb.project_hash"] != "" {
			return "", "", fmt.Errorf("volume %q is %s state but carries project identity labels", name, kind)
		}
		group, logical, ok := strings.Cut(owner, "/")
		if !ok || group == "" || logical == "" || strings.Contains(logical, "/") {
			return "", "", fmt.Errorf("shared volume %q has invalid cb.owner %q", name, owner)
		}
		expected := pathmap.StatefulSharedVolumeID(group, logical)
		if owner == "python313/pip-cache" {
			expected = "cb-pip-cache"
		}
		if name != expected {
			return "", "", fmt.Errorf("shared volume %q does not match owner identity (expected %q)", name, expected)
		}
		return "", "", nil
	case "compat":
		if labels["cb.project_path"] != "" || labels["cb.project_hash"] != "" {
			return "", "", fmt.Errorf("volume %q is %s state but carries project identity labels", name, kind)
		}
		if name != "cb-python-313-global" || owner != "python313/global" {
			return "", "", fmt.Errorf("compat volume %q has unsupported owner identity %q", name, owner)
		}
		return "", "", nil
	case "project":
		projectPath := labels["cb.project_path"]
		if projectPath == "" {
			return "", "", fmt.Errorf("project volume %q has no cb.project_path label", name)
		}
		projectHash := pathmap.VolumeHash(projectPath)
		if labeled := labels["cb.project_hash"]; labeled != "" && labeled != projectHash {
			return "", "", fmt.Errorf("project volume %q has inconsistent cb.project_hash", name)
		}
		group, logical, ok := strings.Cut(owner, "/")
		if !ok || group == "" || logical == "" || strings.Contains(logical, "/") {
			return "", "", fmt.Errorf("project volume %q has invalid cb.owner %q", name, owner)
		}
		expected := pathmap.StatefulProjectVolumeID(group, logical, projectPath, true)
		if owner == "python313/venv" {
			expected = pathmap.PythonEnvID(projectPath, true)
		}
		if name != expected {
			return "", "", fmt.Errorf("project volume %q does not match owner/path identity (expected %q)", name, expected)
		}
		return projectPath, projectHash, nil
	default:
		return "", "", fmt.Errorf("volume %q has unsupported cb.kind %q", name, kind)
	}
}

func ensureHelperImage() error {
	cmd := exec.Command("docker", "image", "inspect", HelperImage)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("state archive helper image is not local: %s; load or pull %s explicitly before retrying: %w", strings.TrimSpace(string(out)), HelperImage, err)
	}
	return nil
}

func requireQuiescent(name string) error {
	cmd := exec.Command("docker", "ps", "--filter", "volume="+name, "--format", "{{.ID}} {{.Names}}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("check active writers for %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	if active := strings.TrimSpace(string(out)); active != "" {
		return fmt.Errorf("volume %q is mounted by a running container (%s); stop the writer and retry", name, strings.ReplaceAll(active, "\n", ", "))
	}
	return nil
}

func helperCommand(volume string, readOnly, stdin bool, command ...string) *exec.Cmd {
	mode := ""
	if readOnly {
		mode = ",readonly"
	}
	args := []string{"run", "--rm"}
	if stdin {
		args = append(args, "-i")
	}
	args = append(args, "--pull", "never", "--network", "none", "--read-only", "--mount", "type=volume,src="+volume+",dst=/volume"+mode, HelperImage)
	args = append(args, command...)
	return exec.Command("docker", args...)
}

func exportVolume(name string, dst io.Writer) error {
	cmd := helperCommand(name, true, false, "tar", "-C", "/volume", "-cf", "-", ".")
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = dst, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("archive volume %s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func importVolume(name string, src io.Reader) error {
	cmd := helperCommand(name, false, true, "tar", "-C", "/volume", "-xf", "-")
	var stderr bytes.Buffer
	cmd.Stdin, cmd.Stderr = src, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("restore volume %s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func volumeEmpty(name string) (bool, error) {
	cmd := helperCommand(name, true, false, "find", "/volume", "-mindepth", "1", "-print", "-quit")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("inspect volume %s contents: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)) == "", nil
}

func validateVolumeTar(zf *zip.File, volume *Volume) error {
	r, err := zf.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	h := sha256.New()
	counter := &countingWriter{w: h}
	tee := io.TeeReader(r, counter)
	tr := tar.NewReader(tee)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("volume %q contains an invalid tar stream: %w", volume.Name, err)
		}
		if err := validateTarHeader(header); err != nil {
			return fmt.Errorf("volume %q: %w", volume.Name, err)
		}
	}
	if _, err := io.Copy(io.Discard, tee); err != nil {
		return err
	}
	if counter.n != volume.ArchiveSize || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), volume.SHA256) {
		return fmt.Errorf("volume %q archive checksum/size does not match the manifest", volume.Name)
	}
	return nil
}

func validateTarHeader(header *tar.Header) error {
	if header.Name == "" || strings.ContainsRune(header.Name, 0) {
		return errors.New("tar entry has an empty or NUL-containing name")
	}
	clean := path.Clean(header.Name)
	if path.IsAbs(header.Name) || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("tar entry %q escapes the volume root", header.Name)
	}
	switch header.Typeflag {
	case tar.TypeReg, tar.TypeRegA, tar.TypeDir, tar.TypeSymlink, tar.TypeLink:
		return nil
	default:
		return fmt.Errorf("tar entry %q uses unsupported type %d", header.Name, header.Typeflag)
	}
}
