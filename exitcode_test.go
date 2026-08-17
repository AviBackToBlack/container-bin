package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestFatalfExitsCbFailure(t *testing.T) {
	orig := osExit
	var code int
	var calls int
	osExit = func(c int) {
		code = c
		calls++
	}
	t.Cleanup(func() { osExit = orig })

	fatalf("boom: %v", errors.New("x"))
	if calls != 1 {
		t.Fatalf("osExit called %d times, want 1", calls)
	}
	if code != exitCbFailure {
		t.Fatalf("got exit code %d, want %d", code, exitCbFailure)
	}
}

func TestExitAndInterruptConstants(t *testing.T) {
	if exitUsage != 2 {
		t.Errorf("exitUsage = %d, want 2", exitUsage)
	}
	if exitCbFailure != 120 {
		t.Errorf("exitCbFailure = %d, want 120", exitCbFailure)
	}
	if exitInterrupted != 130 {
		t.Errorf("exitInterrupted = %d, want 130", exitInterrupted)
	}
}

func TestExitCodeDefault(t *testing.T) {
	// osExit is the indirection every exit-code decision uses in unit tests.
	// A leaked stub would make later tests silently swallow real exits.
	if reflect.ValueOf(osExit).Pointer() != reflect.ValueOf(os.Exit).Pointer() {
		t.Error("osExit does not default to os.Exit")
	}
}

func TestSubprocessExitCodes(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH:", err)
	}

	cbPath := buildTestCb(t)
	workDir := t.TempDir()

	tests := []struct {
		name    string
		args    []string
		workDir string
		want    int
	}{
		{
			name:    "unknown subcommand exits 2",
			args:    []string{"bogus-subcommand"},
			workDir: filepath.Join(workDir, "unknown"),
			want:    exitUsage,
		},
		{
			name:    "unexpose with no args exits 120",
			args:    []string{"unexpose"},
			workDir: filepath.Join(workDir, "unexpose"),
			want:    exitCbFailure,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.MkdirAll(tt.workDir, 0755); err != nil {
				t.Fatalf("mkdir workdir: %v", err)
			}
			runExitTest(t, cbPath, tt.args, tt.want, tt.workDir)
		})
	}
}

// buildTestCb builds a temporary cb binary for the subprocess exit-code
// tests. It requires `go` on PATH and a writable module/build cache. It
// also relies on registryPath() resolving relative to the spawned
// executable's directory, so the test runs without a real
// container-bin.toml and falls back to defaultRegistry(). If
// registryPath() ever changes to a user-profile location, this test could
// start creating container-bin.mutation.lock in the developer's real
// registry.
func buildTestCb(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	testBin := "cb_test"
	cbBin := "cb"
	if runtime.GOOS == "windows" {
		testBin += ".exe"
		cbBin += ".exe"
	}
	testPath := filepath.Join(dir, testBin)
	cbPath := filepath.Join(dir, cbBin)

	build := exec.Command("go", "build", "-o", testPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	// main() only exposes the management CLI when argv[0] is "cb",
	// "container-bin", or starts with "cb-v". Build the artifact as cb_test,
	// then create an executable named cb in the same temp directory.
	if err := os.Link(testPath, cbPath); err != nil {
		if err := copyFile(testPath, cbPath); err != nil {
			t.Fatalf("create cb copy: %v", err)
		}
	}
	return cbPath
}

func runExitTest(t *testing.T, cbPath string, args []string, want int, workDir string) {
	t.Helper()
	cmd := exec.Command(cbPath, args...)
	cmd.Dir = workDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil && !isExitError(err) {
		t.Fatalf("run %v: %v\nstderr: %s", args, err, stderr.String())
	}
	if cmd.ProcessState == nil {
		t.Fatal("no process state")
	}
	if got := cmd.ProcessState.ExitCode(); got != want {
		t.Fatalf("run %v exit code = %d, want %d\nstdout: %s\nstderr: %s", args, got, want, stdout.String(), stderr.String())
	}
}

func isExitError(err error) bool {
	_, ok := err.(*exec.ExitError)
	return ok
}
