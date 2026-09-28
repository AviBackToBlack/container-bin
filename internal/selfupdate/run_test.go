package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateCommandCheckStopsBeforeApplyPipeline(t *testing.T) {
	plan := stagingPlan()
	var out bytes.Buffer
	command := updateCommand{
		plan: func(_ context.Context, current, goos, goarch string, opts Options) (Plan, error) {
			if current != plan.Current || goos != "windows" || goarch != "amd64" || !opts.Check {
				t.Fatalf("planner inputs = %q %s/%s %+v", current, goos, goarch, opts)
			}
			return plan, nil
		},
		currentExecutable: func() (string, error) { t.Fatal("check resolved executable"); return "", nil },
		stage: func(context.Context, Plan, string) (Staged, error) {
			t.Fatal("check staged files")
			return Staged{}, nil
		},
		verify: func(context.Context, Plan, string, string, string, string) (Verified, error) {
			t.Fatal("check verified files")
			return Verified{}, nil
		},
		launch: func(context.Context, Plan, Staged, Verified, string, string) error {
			t.Fatal("check launched helper")
			return nil
		},
		goos:   "windows",
		goarch: "amd64",
	}
	if err := command.run(context.Background(), plan.Current, Options{Check: true}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "read-only; no files changed") {
		t.Fatalf("check output = %q", out.String())
	}
}

func TestUpdateCommandApplyStagesVerifiesAndTransfersOwnership(t *testing.T) {
	_, installed := testInstallation(t)
	gh := filepath.Join(t.TempDir(), "gh.exe")
	if err := os.WriteFile(gh, []byte("GitHub CLI"), 0o700); err != nil {
		t.Fatal(err)
	}
	canonicalGH, _, err := canonicalVerificationFile(gh, "GitHub CLI fixture")
	if err != nil {
		t.Fatal(err)
	}
	gh = canonicalGH
	plan := stagingPlan()
	stageDir, err := os.MkdirTemp(filepath.Dir(installed), stagingPrefix)
	if err != nil {
		t.Fatal(err)
	}
	staged := Staged{
		Dir:           stageDir,
		BinaryPath:    filepath.Join(stageDir, plan.Binary.Name),
		ChecksumsPath: filepath.Join(stageDir, plan.Checksums.Name),
		Target:        plan.Target,
		owned:         true,
	}
	for _, path := range []string{staged.BinaryPath, staged.ChecksumsPath} {
		if err := os.WriteFile(path, []byte("staged"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	verified := Verified{target: plan.Target}
	launched := false
	command := updateCommand{
		plan:              func(context.Context, string, string, string, Options) (Plan, error) { return plan, nil },
		currentExecutable: func() (string, error) { return installed, nil },
		stage: func(_ context.Context, got Plan, executable string) (Staged, error) {
			if got.Target != plan.Target || executable != installed {
				t.Fatalf("stage inputs = %+v, %q", got, executable)
			}
			return staged, nil
		},
		verify: func(_ context.Context, got Plan, binary, checksums, executable, githubCLI string) (Verified, error) {
			if got.Target != plan.Target || binary != staged.BinaryPath || checksums != staged.ChecksumsPath || executable != installed || githubCLI != gh {
				t.Fatalf("verify inputs = %+v, %q, %q, %q, %q", got, binary, checksums, executable, githubCLI)
			}
			return verified, nil
		},
		launch: func(_ context.Context, got Plan, gotStaged Staged, gotVerified Verified, executable, githubCLI string) error {
			launched = true
			if got.Target != plan.Target || gotStaged.Dir != staged.Dir || !gotStaged.owned || gotVerified.target != plan.Target || executable != installed || githubCLI != gh {
				t.Fatalf("launch inputs = %+v, %+v, %+v, %q, %q", got, gotStaged, gotVerified, executable, githubCLI)
			}
			return nil
		},
		goos: "windows", goarch: "amd64",
	}
	var out bytes.Buffer
	opts := Options{Apply: true, GitHubCLI: gh}
	if err := command.run(context.Background(), plan.Current, opts, &out); err != nil {
		t.Fatal(err)
	}
	if !launched || !strings.Contains(out.String(), "private helper launched") {
		t.Fatalf("apply launch=%t output=%q", launched, out.String())
	}
	if _, err := os.Stat(stageDir); err != nil {
		t.Fatalf("parent cleaned staging after transferring ownership: %v", err)
	}
}

func TestUpdateCommandApplyCleansStagingBeforeHelperOwnership(t *testing.T) {
	_, installed := testInstallation(t)
	gh := filepath.Join(t.TempDir(), "gh.exe")
	if err := os.WriteFile(gh, []byte("GitHub CLI"), 0o700); err != nil {
		t.Fatal(err)
	}
	plan := stagingPlan()
	stageDir, err := os.MkdirTemp(filepath.Dir(installed), stagingPrefix)
	if err != nil {
		t.Fatal(err)
	}
	staged := Staged{
		Dir: stageDir, BinaryPath: filepath.Join(stageDir, "cb.exe"),
		ChecksumsPath: filepath.Join(stageDir, "SHA256SUMS"), Target: plan.Target, owned: true,
	}
	for _, path := range []string{staged.BinaryPath, staged.ChecksumsPath} {
		if err := os.WriteFile(path, []byte("staged"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want := errors.New("verification failed")
	command := updateCommand{
		plan:              func(context.Context, string, string, string, Options) (Plan, error) { return plan, nil },
		currentExecutable: func() (string, error) { return installed, nil },
		stage:             func(context.Context, Plan, string) (Staged, error) { return staged, nil },
		verify:            func(context.Context, Plan, string, string, string, string) (Verified, error) { return Verified{}, want },
		launch: func(context.Context, Plan, Staged, Verified, string, string) error {
			t.Fatal("failed verification launched helper")
			return nil
		},
		goos: "windows", goarch: "amd64",
	}
	err = command.run(context.Background(), plan.Current, Options{Apply: true, GitHubCLI: gh}, &bytes.Buffer{})
	if !errors.Is(err, want) {
		t.Fatalf("apply error = %v", err)
	}
	if _, statErr := os.Stat(stageDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed apply left staging directory: %v", statErr)
	}
}

func TestUpdateCommandCurrentDoesNotRequireVerifierOrStage(t *testing.T) {
	_, installed := testInstallation(t)
	gh := filepath.Join(t.TempDir(), "gh.exe")
	if err := os.WriteFile(gh, []byte("GitHub CLI"), 0o700); err != nil {
		t.Fatal(err)
	}
	plan := stagingPlan()
	plan.Target, plan.Status = plan.Current, "CURRENT"
	command := updateCommand{
		plan:              func(context.Context, string, string, string, Options) (Plan, error) { return plan, nil },
		currentExecutable: func() (string, error) { return installed, nil },
		stage: func(context.Context, Plan, string) (Staged, error) {
			t.Fatal("current release staged files")
			return Staged{}, nil
		},
		verify: func(context.Context, Plan, string, string, string, string) (Verified, error) {
			t.Fatal("current release invoked verifier")
			return Verified{}, nil
		},
		launch: func(context.Context, Plan, Staged, Verified, string, string) error {
			t.Fatal("current release launched helper")
			return nil
		},
		goos: "windows", goarch: "amd64",
	}
	var out bytes.Buffer
	if err := command.run(context.Background(), plan.Current, Options{Apply: true, GitHubCLI: gh}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "already current; no files changed") {
		t.Fatalf("current output = %q", out.String())
	}
}

func TestUpdateCommandApplyRejectsInvalidGitHubCLIBeforePlanning(t *testing.T) {
	_, installed := testInstallation(t)
	planned := false
	command := updateCommand{
		plan: func(context.Context, string, string, string, Options) (Plan, error) {
			planned = true
			return Plan{}, nil
		},
		currentExecutable: func() (string, error) { return installed, nil },
		stage:             func(context.Context, Plan, string) (Staged, error) { return Staged{}, nil },
		verify:            func(context.Context, Plan, string, string, string, string) (Verified, error) { return Verified{}, nil },
		launch:            func(context.Context, Plan, Staged, Verified, string, string) error { return nil },
		goos:              "windows", goarch: "amd64",
	}
	err := command.run(context.Background(), "v1.1.0", Options{Apply: true, GitHubCLI: filepath.Join(t.TempDir(), "missing-gh.exe")}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "GitHub CLI executable") || planned {
		t.Fatalf("invalid GitHub CLI error=%v planned=%t", err, planned)
	}
}

func TestUpdateCommandApplyRejectsNonManagementExecutableBeforePlanning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "go.exe")
	if err := os.WriteFile(path, []byte("shim"), 0o700); err != nil {
		t.Fatal(err)
	}
	planned := false
	command := updateCommand{
		plan: func(context.Context, string, string, string, Options) (Plan, error) {
			planned = true
			return Plan{}, nil
		},
		currentExecutable: func() (string, error) { return path, nil },
		stage:             func(context.Context, Plan, string) (Staged, error) { return Staged{}, nil },
		verify:            func(context.Context, Plan, string, string, string, string) (Verified, error) { return Verified{}, nil },
		launch:            func(context.Context, Plan, Staged, Verified, string, string) error { return nil },
		goos:              "windows", goarch: "amd64",
	}
	err := command.run(context.Background(), "v1.1.0", Options{Apply: true, GitHubCLI: "gh.exe"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "must be invoked from cb.exe") || planned {
		t.Fatalf("non-management apply error=%v planned=%t", err, planned)
	}
}
