package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyTransactionReplacesCompleteProvenManagedSet(t *testing.T) {
	fixture := newApplyFixture(t)
	copyShim := filepath.Join(fixture.dir, "gofmt.exe")
	writeApplyFile(t, copyShim, fixture.oldBytes)
	unrelated := filepath.Join(fixture.dir, "unrelated.exe")
	writeApplyFile(t, unrelated, []byte("not ContainerBin"))
	reserved := filepath.Join(fixture.dir, "cb-v1.0.0.exe")
	writeApplyFile(t, reserved, fixture.oldBytes)

	var smoked bool
	tx := applyTransaction{
		replace: replaceManagedFile,
		smoke: func(_ context.Context, executable, target string) error {
			smoked = true
			if executable != fixture.installed || target != "v1.1.0" {
				t.Fatalf("smoke inputs = %q, %q", executable, target)
			}
			assertApplyBytes(t, executable, fixture.newBytes)
			assertApplyBytes(t, fixture.hardlinkShim, fixture.newBytes)
			assertApplyBytes(t, copyShim, fixture.newBytes)
			return nil
		},
	}
	if err := tx.apply(context.Background(), fixture.verified, fixture.installed); err != nil {
		t.Fatal(err)
	}
	if !smoked {
		t.Fatal("updated executable was not smoke tested")
	}
	assertApplyBytes(t, fixture.installed, fixture.newBytes)
	assertApplyBytes(t, fixture.hardlinkShim, fixture.newBytes)
	assertApplyBytes(t, copyShim, fixture.newBytes)
	assertApplyBytes(t, unrelated, []byte("not ContainerBin"))
	assertApplyBytes(t, reserved, fixture.oldBytes)
	assertNoApplyRecoveryArtifacts(t, fixture.dir)

	installedInfo, err := os.Stat(fixture.installed)
	if err != nil {
		t.Fatal(err)
	}
	for _, shim := range []string{fixture.hardlinkShim, copyShim} {
		shimInfo, err := os.Stat(shim)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(installedInfo, shimInfo) {
			t.Errorf("managed shim %s was not reconciled as a hardlink", shim)
		}
	}
}

func TestApplyTransactionRollsBackCompleteManagedSetAfterSmokeFailure(t *testing.T) {
	fixture := newApplyFixture(t)
	copyShim := filepath.Join(fixture.dir, "gofmt.exe")
	writeApplyFile(t, copyShim, fixture.oldBytes)
	tx := applyTransaction{
		replace: replaceManagedFile,
		smoke: func(context.Context, string, string) error {
			return errors.New("new binary did not start")
		},
	}
	err := tx.apply(context.Background(), fixture.verified, fixture.installed)
	if err == nil || !strings.Contains(err.Error(), "restored to their previous bytes") {
		t.Fatalf("apply error = %v, want successful rollback evidence", err)
	}
	for _, path := range []string{fixture.installed, fixture.hardlinkShim, copyShim} {
		assertApplyBytes(t, path, fixture.oldBytes)
	}
	installedInfo, err := os.Stat(fixture.installed)
	if err != nil {
		t.Fatal(err)
	}
	for _, shim := range []string{fixture.hardlinkShim, copyShim} {
		shimInfo, err := os.Stat(shim)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(installedInfo, shimInfo) {
			t.Errorf("rollback shim %s was not reconciled to the restored management executable", shim)
		}
	}
	assertNoApplyRecoveryArtifacts(t, fixture.dir)
}

func TestApplyTransactionRollsBackReplacementThatReportedFailureAfterChangingBytes(t *testing.T) {
	fixture := newApplyFixture(t)
	first := true
	tx := applyTransaction{
		replace: func(source, destination string, preferHardlink bool) error {
			if err := replaceManagedFile(source, destination, preferHardlink); err != nil {
				return err
			}
			if first {
				first = false
				return errors.New("ambiguous replacement result")
			}
			return nil
		},
		smoke: func(context.Context, string, string) error {
			t.Fatal("ambiguous replacement reached smoke test")
			return nil
		},
	}
	err := tx.apply(context.Background(), fixture.verified, fixture.installed)
	if err == nil || !strings.Contains(err.Error(), "reported failure after changing bytes") || !strings.Contains(err.Error(), "restored to their previous bytes") {
		t.Fatalf("apply error = %v", err)
	}
	assertApplyBytes(t, fixture.installed, fixture.oldBytes)
	assertApplyBytes(t, fixture.hardlinkShim, fixture.oldBytes)
	assertNoApplyRecoveryArtifacts(t, fixture.dir)
}

func TestApplyTransactionPreservesRecoveryAfterInconclusiveReplacementFailure(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
		want   string
	}{
		{
			name: "destination unreadable",
			mutate: func(t *testing.T, destination string) {
				t.Helper()
				if err := os.Remove(destination); err != nil {
					t.Fatal(err)
				}
			},
			want: "inspect installed management executable after reported replacement failure",
		},
		{
			name: "destination has unknown bytes",
			mutate: func(t *testing.T, destination string) {
				t.Helper()
				writeApplyFileReplace(t, destination, []byte("unknown post-replacement bytes"))
			},
			want: "neither the previous nor verified update bytes",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newApplyFixture(t)
			first := true
			tx := applyTransaction{
				replace: func(_ string, destination string, _ bool) error {
					if first {
						first = false
						tc.mutate(t, destination)
						return errors.New("ambiguous replacement result")
					}
					return errors.New("unexpected rollback replacement")
				},
				smoke: func(context.Context, string, string) error {
					t.Fatal("inconclusive replacement reached smoke test")
					return nil
				},
			}
			err := tx.apply(context.Background(), fixture.verified, fixture.installed)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "recovery executable preserved") {
				t.Fatalf("apply error = %v", err)
			}
			matches, globErr := filepath.Glob(filepath.Join(fixture.dir, rollbackPrefix+"*", "cb.exe"))
			if globErr != nil {
				t.Fatal(globErr)
			}
			if len(matches) != 1 {
				t.Fatalf("recovery executables = %v, want one", matches)
			}
			assertApplyBytes(t, matches[0], fixture.oldBytes)
		})
	}
}

func TestApplyTransactionRejectsChangedVerifiedBytesBeforeInstalledMutation(t *testing.T) {
	fixture := newApplyFixture(t)
	writeApplyFileReplace(t, fixture.verified.binaryPath, []byte("changed after attestation"))
	smoked := false
	tx := applyTransaction{
		replace: replaceManagedFile,
		smoke: func(context.Context, string, string) error {
			smoked = true
			return nil
		},
	}
	err := tx.apply(context.Background(), fixture.verified, fixture.installed)
	if err == nil || !strings.Contains(err.Error(), "changed before replacement") {
		t.Fatalf("apply error = %v", err)
	}
	if smoked {
		t.Fatal("changed staged bytes reached the smoke test")
	}
	assertApplyBytes(t, fixture.installed, fixture.oldBytes)
	assertApplyBytes(t, fixture.hardlinkShim, fixture.oldBytes)
	assertNoApplyRecoveryArtifacts(t, fixture.dir)
}

func TestApplyTransactionRejectsInstalledExecutableChangedAfterVerification(t *testing.T) {
	fixture := newApplyFixture(t)
	writeApplyFileReplace(t, fixture.installed, []byte("newer independently installed ContainerBin executable"))
	replaced := false
	tx := applyTransaction{
		replace: func(string, string, bool) error {
			replaced = true
			return nil
		},
		smoke: func(context.Context, string, string) error {
			t.Fatal("stale update reached smoke test")
			return nil
		},
	}
	err := tx.apply(context.Background(), fixture.verified, fixture.installed)
	if err == nil || !strings.Contains(err.Error(), "changed after update verification") {
		t.Fatalf("apply error = %v", err)
	}
	if replaced {
		t.Fatal("stale update attempted an installed-file replacement")
	}
	assertNoApplyRecoveryArtifacts(t, fixture.dir)
}

func TestReplaceManagedFileReplacesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.exe")
	destination := filepath.Join(dir, "destination.exe")
	writeApplyFile(t, source, []byte("new bytes"))
	writeApplyFile(t, destination, []byte("old bytes"))
	if err := replaceManagedFile(source, destination, false); err != nil {
		t.Fatal(err)
	}
	assertApplyBytes(t, source, []byte("new bytes"))
	assertApplyBytes(t, destination, []byte("new bytes"))
}

func TestApplyTransactionPreservesRecoveryArtifactWhenRollbackFails(t *testing.T) {
	fixture := newApplyFixture(t)
	replacements := 0
	tx := applyTransaction{
		replace: func(source, destination string, preferHardlink bool) error {
			replacements++
			if replacements >= 3 {
				return errors.New("filesystem denied rollback")
			}
			return replaceManagedFile(source, destination, preferHardlink)
		},
		smoke: func(context.Context, string, string) error {
			return errors.New("smoke failure")
		},
	}
	err := tx.apply(context.Background(), fixture.verified, fixture.installed)
	if err == nil || !strings.Contains(err.Error(), "recovery executable preserved") {
		t.Fatalf("apply error = %v", err)
	}
	matches, globErr := filepath.Glob(filepath.Join(fixture.dir, rollbackPrefix+"*", "cb.exe"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 1 {
		t.Fatalf("recovery executables = %v, want one", matches)
	}
	assertApplyBytes(t, matches[0], fixture.oldBytes)
}

func TestApplyTransactionRefusesForeignShimChangeDuringRollback(t *testing.T) {
	fixture := newApplyFixture(t)
	foreignBytes := []byte("foreign concurrent shim replacement")
	tx := applyTransaction{
		replace: replaceManagedFile,
		smoke: func(context.Context, string, string) error {
			writeApplyFileReplace(t, fixture.hardlinkShim, foreignBytes)
			return errors.New("smoke failure after foreign shim change")
		},
	}
	err := tx.apply(context.Background(), fixture.verified, fixture.installed)
	if err == nil || !strings.Contains(err.Error(), "changed outside the update transaction") || !strings.Contains(err.Error(), "recovery executable preserved") {
		t.Fatalf("apply error = %v", err)
	}
	assertApplyBytes(t, fixture.installed, fixture.oldBytes)
	assertApplyBytes(t, fixture.hardlinkShim, foreignBytes)
	assertOneApplyRecoveryExecutable(t, fixture.dir, fixture.oldBytes)
}

func TestApplyTransactionUsesRecoveryCopyWhenManagementRestoreFails(t *testing.T) {
	fixture := newApplyFixture(t)
	fallbackUsed := false
	tx := applyTransaction{
		replace: func(source, destination string, preferHardlink bool) error {
			fromRecovery := strings.HasPrefix(filepath.Base(filepath.Dir(source)), rollbackPrefix)
			if fromRecovery && destination == fixture.installed {
				return errors.New("filesystem denied management restore")
			}
			if fromRecovery && destination == fixture.hardlinkShim {
				fallbackUsed = true
				if preferHardlink {
					t.Fatal("defensive recovery fallback preferred a hardlink")
				}
			}
			return replaceManagedFile(source, destination, preferHardlink)
		},
		smoke: func(context.Context, string, string) error {
			return errors.New("smoke failure")
		},
	}
	err := tx.apply(context.Background(), fixture.verified, fixture.installed)
	if err == nil || !strings.Contains(err.Error(), "restore management executable") || !strings.Contains(err.Error(), "recovery executable preserved") {
		t.Fatalf("apply error = %v", err)
	}
	if !fallbackUsed {
		t.Fatal("rollback did not use the defensive recovery-copy fallback")
	}
	assertApplyBytes(t, fixture.installed, fixture.newBytes)
	assertApplyBytes(t, fixture.hardlinkShim, fixture.oldBytes)
	assertOneApplyRecoveryExecutable(t, fixture.dir, fixture.oldBytes)
}

type applyFixture struct {
	dir          string
	installed    string
	hardlinkShim string
	verified     Verified
	oldBytes     []byte
	newBytes     []byte
}

func newApplyFixture(t *testing.T) applyFixture {
	t.Helper()
	dir := t.TempDir()
	var err error
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	oldBytes := []byte("old signed ContainerBin executable")
	newBytes := []byte("new attested ContainerBin executable")
	installed := filepath.Join(dir, "cb.exe")
	writeApplyFile(t, installed, oldBytes)
	hardlinkShim := filepath.Join(dir, "go.exe")
	if err := os.Link(installed, hardlinkShim); err != nil {
		t.Fatal(err)
	}
	stageDir, err := os.MkdirTemp(dir, stagingPrefix)
	if err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(stageDir, "cb.exe")
	writeApplyFile(t, staged, newBytes)
	sum := sha256.Sum256(newBytes)
	oldSum := sha256.Sum256(oldBytes)
	return applyFixture{
		dir:          dir,
		installed:    installed,
		hardlinkShim: hardlinkShim,
		verified: Verified{
			binaryPath:       staged,
			target:           "v1.1.0",
			digest:           hex.EncodeToString(sum[:]),
			size:             int64(len(newBytes)),
			installedPath:    installed,
			installedVersion: "v1.0.0",
			installedDigest:  hex.EncodeToString(oldSum[:]),
			installedSize:    int64(len(oldBytes)),
		},
		oldBytes: oldBytes,
		newBytes: newBytes,
	}
}

func writeApplyFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeApplyFileReplace(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeApplyFile(t, path, data)
}

func assertApplyBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s bytes = %q, want %q", path, got, want)
	}
}

func assertNoApplyRecoveryArtifacts(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, rollbackPrefix+"*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("unexpected rollback artifacts: %v", matches)
	}
}

func assertOneApplyRecoveryExecutable(t *testing.T, dir string, want []byte) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, rollbackPrefix+"*", "cb.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("recovery executables = %v, want one", matches)
	}
	assertApplyBytes(t, matches[0], want)
}
