package pythonenv

import (
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestBootstrapShellQuotingAndArgv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX bootstrap executes inside the Linux tool container")
	}
	// Replace only the image interpreter command with a shell function that
	// prints its arguments. This exercises the actual embedded shell quoting
	// without creating /venv or running the bootstrap on the test host.
	wrapper := `python() { printf '%s\000' "$@"; }; ` + Bootstrap
	wrapper = strings.Replace(wrapper, "exec python", "python", 1)
	args := []string{"-c", "print('quoted')", "a b", "", "$(no-command)"}
	cmd := exec.Command("sh", append([]string{"-c", wrapper, "cb"}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	want := append([]string{"-c", script}, args...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bootstrap argv mismatch: got %q, want %q", got, want)
	}
}
