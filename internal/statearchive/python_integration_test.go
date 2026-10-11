package statearchive

import (
	"archive/zip"
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/AviBackToBlack/container-bin/internal/dockervol"
	"github.com/AviBackToBlack/container-bin/internal/pathmap"
	"github.com/AviBackToBlack/container-bin/internal/pythonenv"
)

// This opt-in test requires the two pinned images to be local. It never pulls,
// adopts existing volumes, or mounts host projects. CI runs all six cases on
// its disposable daemon; a developer can select only project cases when the
// fixed compatibility volume already contains real state.
func TestPythonStateDockerRoundTrip(t *testing.T) {
	if os.Getenv("CB_STATE_DOCKER_TEST") != "1" {
		t.Skip("set CB_STATE_DOCKER_TEST=1 on an isolated Docker environment")
	}
	const pythonImage = "python@sha256:bf44cdfcb76cd3b41e879bc058fc37ec5872002ccfde7fcb765e218cde0cd79c"
	for _, image := range []string{pythonImage, HelperImage} {
		if out, err := exec.Command("docker", "image", "inspect", image).CombinedOutput(); err != nil {
			t.Fatalf("required pinned image is not local: %s: %v: %s", image, err, out)
		}
	}
	for _, kind := range []string{"project", "compat"} {
		for _, age := range []string{"fresh", "legacy", "partial"} {
			t.Run(kind+"-"+age, func(t *testing.T) {
				project := t.TempDir()
				name := pathmap.PythonEnvID(project, kind == "project")
				labels := map[string]string{
					"cb.managed": "true", "cb.kind": kind, "cb.owner": "python313/venv",
					"cb.test_id": pathmap.VolumeHash(project),
				}
				if kind == "project" {
					labels["cb.project_path"] = project
					labels["cb.project_hash"] = pathmap.VolumeHash(project)
				}
				existing, err := dockervol.ExistsSet()
				if err != nil {
					t.Fatal(err)
				}
				if existing[name] {
					t.Fatalf("refusing to adopt existing volume %s", name)
				}
				if err := dockervol.EnsureManaged(name, labels); err != nil {
					t.Fatal(err)
				}
				assertOwned := func() {
					t.Helper()
					actual, err := dockervol.Labels(name)
					if err != nil || actual["cb.test_id"] != labels["cb.test_id"] {
						t.Fatalf("test ownership changed for %s: %v", name, err)
					}
				}
				assertOwned()
				t.Cleanup(func() {
					assertOwned()
					if err := dockervol.Remove(name); err != nil {
						t.Error(err)
					}
				})
				runPython := func(bootstrap string, args ...string) string {
					t.Helper()
					assertOwned()
					argv := []string{"run", "--rm", "--pull", "never", "--network", "none", "--mount", "type=volume,src=" + name + ",dst=/venv", "--env", "VIRTUAL_ENV=/venv", "--env", "PATH=/venv/bin:/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin", pythonImage, "sh", "-c", bootstrap, "cb"}
					out, err := exec.Command("docker", append(argv, args...)...).CombinedOutput()
					if err != nil {
						t.Fatalf("Python fixture failed: %v: %s", err, out)
					}
					return strings.TrimSpace(string(out))
				}
				if age != "fresh" {
					runPython(`python -m venv /venv`)
				}
				if age == "partial" {
					runPython(`python -c 'import pathlib,shutil; image=pathlib.Path("/usr/local/bin/python3.13"); alias=pathlib.Path("/venv/bin/python"); alias.unlink(); shutil.copy2(image,alias); alias=pathlib.Path("/venv/bin/python3"); alias.unlink(); alias.symlink_to(image)'`)
				}
				runPython(pythonenv.Bootstrap, "-c", `import pathlib,sysconfig; pathlib.Path(sysconfig.get_path("purelib"), "cb_roundtrip_fixture.py").write_text("value = 42\n")`)
				// Exercise the pip sentinel against the real copied interpreter.
				if out := runPython(pythonenv.Bootstrap, "__CB_PIP__", "--version"); !strings.Contains(out, "/venv/") {
					t.Fatalf("pip did not run in the venv: %s", out)
				}
				var buffer bytes.Buffer
				writer := zip.NewWriter(&buffer)
				if _, err := Backup(writer, []string{name}, "integration", time.Now()); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				reader, err := zip.NewReader(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
				if err != nil {
					t.Fatal(err)
				}
				archive, err := Open(reader.File)
				if err != nil {
					t.Fatalf("producer output rejected by consumer: %v", err)
				}
				if _, err := archive.Plan(); err == nil || !strings.Contains(err.Error(), "not empty") {
					t.Fatalf("nonempty destination was not refused: %v", err)
				}
				assertOwned()
				if err := dockervol.Remove(name); err != nil {
					t.Fatal(err)
				}
				if err := archive.Restore(); err != nil {
					t.Fatal(err)
				}
				if out := runPython(pythonenv.Bootstrap, "-c", "import cb_roundtrip_fixture; print(cb_roundtrip_fixture.value)"); out != "42" {
					t.Fatalf("restored package state lost: %s", out)
				}
				streamArgs := []string{"run", "--rm", "-i", "--pull", "never", "--network", "none", "--mount", "type=volume,src=" + name + ",dst=/venv", "--env", "VIRTUAL_ENV=/venv", "--env", "PATH=/venv/bin:/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin", pythonImage, "sh", "-c", pythonenv.Bootstrap, "cb", "-c",
					`import sys; sys.stdout.write(sys.stdin.read()); sys.stderr.write("stderr-only\n"); sys.exit(42)`}
				stream := exec.Command("docker", streamArgs...)
				stream.Stdin = strings.NewReader("stdin-through-EOF\n")
				var stdout, stderr bytes.Buffer
				stream.Stdout, stream.Stderr = &stdout, &stderr
				exit, ok := stream.Run().(*exec.ExitError)
				if !ok || exit.ExitCode() != 42 || stdout.String() != "stdin-through-EOF\n" || stderr.String() != "stderr-only\n" {
					t.Fatalf("bootstrap stream/exit contract failed: %v; stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
				}
			})
		}
	}
}
