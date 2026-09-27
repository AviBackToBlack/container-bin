package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestIsHelperInvocationIsExact(t *testing.T) {
	for _, tc := range []struct {
		invoked string
		args    []string
		want    bool
	}{
		{"cb-update-helper", []string{"__self-update-helper", "--request", `C:\request.json`}, true},
		{"CB-UPDATE-HELPER", []string{"__self-update-helper"}, true},
		{"cb-update-helper", nil, false},
		{"cb-update-helper", []string{"version"}, false},
		{"cb", []string{"__self-update-helper"}, false},
		{"node", []string{"__self-update-helper"}, false},
	} {
		if got := IsHelperInvocation(tc.invoked, tc.args); got != tc.want {
			t.Errorf("IsHelperInvocation(%q, %q) = %t, want %t", tc.invoked, tc.args, got, tc.want)
		}
	}
}

func TestHelperCoordinatorCreatesBoundedPrivateRequest(t *testing.T) {
	fixture := newHelperFixture(t)
	var helperExecutable, requestPath string
	coordinator := helperCoordinator{
		restrict: func(string, bool) error { return nil },
		start: func(executable, request string) error {
			helperExecutable, requestPath = executable, request
			return nil
		},
	}
	if err := coordinator.launch(fixture.plan, fixture.staged, fixture.verified, fixture.installed, fixture.gh, 4242); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(helperExecutable)) })
	if filepath.Base(helperExecutable) != helperExecutableName || filepath.Base(requestPath) != helperRequestName || filepath.Dir(helperExecutable) != filepath.Dir(requestPath) {
		t.Fatalf("helper layout = %q, %q", helperExecutable, requestPath)
	}
	info, err := os.Stat(requestPath)
	if err != nil || info.Size() <= 0 || info.Size() > maxHelperRequestSize {
		t.Fatalf("request size = %v, %v", info, err)
	}
	request, err := loadHelperRequest(requestPath, func() (string, error) { return helperExecutable, nil })
	if err != nil {
		t.Fatal(err)
	}
	if request.ParentPID != 4242 || request.Plan.plan().Target != fixture.plan.Target || !request.Expected.matches(fixture.verified) || !request.Staged.owned {
		t.Fatalf("decoded helper request = %+v", request)
	}
	helperBytes, err := os.ReadFile(helperExecutable)
	if err != nil || !bytes.Equal(helperBytes, fixture.oldBytes) {
		t.Fatalf("helper copy = %q, %v", helperBytes, err)
	}
}

func TestHelperCoordinatorCleansPrivateDirectoryWhenStartFails(t *testing.T) {
	fixture := newHelperFixture(t)
	want := errors.New("start failed")
	err := (helperCoordinator{
		restrict: func(string, bool) error { return nil },
		start:    func(string, string) error { return want },
	}).launch(fixture.plan, fixture.staged, fixture.verified, fixture.installed, fixture.gh, 4242)
	if !errors.Is(err, want) {
		t.Fatalf("launch error = %v", err)
	}
	entries, readErr := os.ReadDir(filepath.Dir(fixture.installed))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), helperDirPrefix) {
			t.Fatalf("failed helper launch left %q", entry.Name())
		}
	}
}

func TestLoadHelperRequestRejectsHelperOutsideInstallDirectory(t *testing.T) {
	fixture := newHelperFixture(t)
	var helperExecutable, requestPath string
	if err := (helperCoordinator{
		restrict: func(string, bool) error { return nil },
		start: func(executable, request string) error {
			helperExecutable, requestPath = executable, request
			return nil
		},
	}).launch(fixture.plan, fixture.staged, fixture.verified, fixture.installed, fixture.gh, 4242); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(helperExecutable)) })

	data, err := os.ReadFile(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	var request helperRequest
	if err := json.Unmarshal(data, &request); err != nil {
		t.Fatal(err)
	}
	foreignInstalled := filepath.Join(t.TempDir(), "cb.exe")
	request.InstalledExecutable = foreignInstalled
	request.Expected.InstalledPath = foreignInstalled
	data, err = json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requestPath, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadHelperRequest(requestPath, func() (string, error) { return helperExecutable, nil }); err == nil || !strings.Contains(err.Error(), "outside the installed executable directory") {
		t.Fatalf("tampered helper request error = %v", err)
	}
}

func TestHelperRunnerWaitsReverifiesAppliesAndTransfersCleanup(t *testing.T) {
	fixture := newHelperFixture(t)
	var helperExecutable, requestPath string
	if err := (helperCoordinator{
		restrict: func(string, bool) error { return nil },
		start: func(executable, request string) error {
			helperExecutable, requestPath = executable, request
			return nil
		},
	}).launch(fixture.plan, fixture.staged, fixture.verified, fixture.installed, fixture.gh, 4242); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(helperExecutable)) })

	var sequence []string
	runner := helperRunner{
		currentExecutable: func() (string, error) { return helperExecutable, nil },
		waitParent: func(waitCtx context.Context, pid int) error {
			sequence = append(sequence, "wait")
			if pid != 4242 {
				t.Fatalf("parent PID = %d", pid)
			}
			if _, ok := waitCtx.Deadline(); !ok {
				t.Fatal("parent wait has no deadline")
			}
			return nil
		},
		verify: func(_ context.Context, plan Plan, binary, checksums, installed, gh string) (Verified, error) {
			sequence = append(sequence, "verify")
			if !reflect.DeepEqual(plan, fixture.plan) || binary != fixture.staged.BinaryPath || checksums != fixture.staged.ChecksumsPath || installed != fixture.installed || gh != fixture.gh {
				t.Fatalf("helper verify inputs = %+v, %q, %q, %q, %q", plan, binary, checksums, installed, gh)
			}
			return fixture.verified, nil
		},
		apply: func(_ context.Context, verified Verified, installed string) error {
			sequence = append(sequence, "apply")
			if !fixture.expected.matches(verified) || installed != fixture.installed {
				t.Fatalf("helper apply inputs = %+v, %q", verified, installed)
			}
			return nil
		},
		cleanupStaged: func(staged Staged) error {
			sequence = append(sequence, "cleanup-stage")
			if staged.Dir != fixture.staged.Dir || !staged.owned {
				t.Fatalf("helper cleanup staging = %+v", staged)
			}
			return nil
		},
		launchCleanup: func(dir string, pid int) error {
			sequence = append(sequence, "cleanup-helper")
			if dir != filepath.Dir(helperExecutable) || pid != os.Getpid() {
				t.Fatalf("helper cleanup inputs = %q, %d", dir, pid)
			}
			return nil
		},
	}
	var out bytes.Buffer
	if err := runner.run(context.Background(), requestPath, &out); err != nil {
		t.Fatal(err)
	}
	wantSequence := []string{"wait", "verify", "apply", "cleanup-stage", "cleanup-helper"}
	if !reflect.DeepEqual(sequence, wantSequence) {
		t.Fatalf("helper sequence = %v, want %v", sequence, wantSequence)
	}
	if !strings.Contains(out.String(), "self-update applied "+fixture.plan.Target) {
		t.Fatalf("helper output = %q", out.String())
	}
}

func TestHelperRunnerRefusesParentBoundVerificationMismatch(t *testing.T) {
	fixture := newHelperFixture(t)
	var helperExecutable, requestPath string
	if err := (helperCoordinator{
		restrict: func(string, bool) error { return nil },
		start: func(executable, request string) error {
			helperExecutable, requestPath = executable, request
			return nil
		},
	}).launch(fixture.plan, fixture.staged, fixture.verified, fixture.installed, fixture.gh, 4242); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(helperExecutable)) })
	changed := fixture.verified
	changed.digest = strings.Repeat("f", 64)
	applied := false
	stagingCleaned := false
	helperCleanupLaunched := false
	err := (helperRunner{
		currentExecutable: func() (string, error) { return helperExecutable, nil },
		waitParent:        func(context.Context, int) error { return nil },
		verify:            func(context.Context, Plan, string, string, string, string) (Verified, error) { return changed, nil },
		apply:             func(context.Context, Verified, string) error { applied = true; return nil },
		cleanupStaged:     func(Staged) error { stagingCleaned = true; return nil },
		launchCleanup:     func(string, int) error { helperCleanupLaunched = true; return nil },
	}).run(context.Background(), requestPath, nil)
	if err == nil || !strings.Contains(err.Error(), "does not match the parent-bound result") || applied || !stagingCleaned || !helperCleanupLaunched {
		t.Fatalf("mismatched helper result error=%v applied=%t staging-cleaned=%t helper-cleanup=%t", err, applied, stagingCleaned, helperCleanupLaunched)
	}
}

func TestParseHelperArgsIsExact(t *testing.T) {
	path := filepath.Join(t.TempDir(), helperRequestName)
	if got, err := parseHelperArgs([]string{"--request", path}); err != nil || got != path {
		t.Fatalf("parseHelperArgs() = %q, %v", got, err)
	}
	for _, args := range [][]string{nil, {"--request"}, {"--request", "relative.json"}, {"--request", filepath.Join(filepath.Dir(path), "other.json")}, {"--other", path}} {
		if _, err := parseHelperArgs(args); err == nil {
			t.Errorf("parseHelperArgs(%q) succeeded", args)
		}
	}
}

type helperFixture struct {
	plan      Plan
	staged    Staged
	verified  Verified
	expected  helperVerification
	installed string
	gh        string
	oldBytes  []byte
}

func newHelperFixture(t *testing.T) helperFixture {
	t.Helper()
	installDir := t.TempDir()
	var err error
	installDir, err = filepath.EvalSymlinks(installDir)
	if err != nil {
		t.Fatal(err)
	}
	oldBytes := []byte("installed ContainerBin helper fixture")
	newBytes := []byte("verified ContainerBin helper target")
	installed := filepath.Join(installDir, "cb.exe")
	gh := filepath.Join(t.TempDir(), "gh.exe")
	for path, contents := range map[string][]byte{installed: oldBytes, gh: []byte("GitHub CLI helper fixture")} {
		if err := os.WriteFile(path, contents, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	gh, _, err = canonicalVerificationFile(gh, "GitHub CLI fixture")
	if err != nil {
		t.Fatal(err)
	}
	stageDir, err := os.MkdirTemp(installDir, stagingPrefix)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(stageDir, "cb.exe")
	checksums := filepath.Join(stageDir, "SHA256SUMS")
	manifest := strings.Repeat("a", 64) + "  cb.exe\n"
	if err := os.WriteFile(binary, newBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checksums, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := stagingPlan()
	plan.Current = "v1.0.0"
	plan.Target = "v1.1.0"
	plan.ReleaseURL = releaseWebRoot + "/tag/v1.1.0"
	plan.Binary = Asset{Name: "cb.exe", URL: releaseWebRoot + "/download/v1.1.0/cb.exe", Size: int64(len(newBytes))}
	plan.Archive = Asset{Name: "container-bin-v1.1.0-windows-amd64.zip", URL: releaseWebRoot + "/download/v1.1.0/container-bin-v1.1.0-windows-amd64.zip", Size: 1}
	plan.Checksums = Asset{Name: "SHA256SUMS", URL: releaseWebRoot + "/download/v1.1.0/SHA256SUMS", Size: int64(len(manifest))}
	plan.ExpectedRef = "refs/tags/v1.1.0"
	newSum, oldSum := sha256.Sum256(newBytes), sha256.Sum256(oldBytes)
	verified := Verified{
		binaryPath: binary, target: plan.Target, digest: hex.EncodeToString(newSum[:]), size: int64(len(newBytes)),
		installedPath: installed, installedVersion: plan.Current, installedDigest: hex.EncodeToString(oldSum[:]), installedSize: int64(len(oldBytes)),
	}
	return helperFixture{
		plan:     plan,
		staged:   Staged{Dir: stageDir, BinaryPath: binary, ChecksumsPath: checksums, Target: plan.Target, owned: true},
		verified: verified, expected: helperVerificationFromVerified(verified), installed: installed, gh: gh, oldBytes: oldBytes,
	}
}
