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
	return applyFixture{
		dir:          dir,
		installed:    installed,
		hardlinkShim: hardlinkShim,
		verified: Verified{
			binaryPath: staged,
			target:     "v1.1.0",
			digest:     hex.EncodeToString(sum[:]),
			size:       int64(len(newBytes)),
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
