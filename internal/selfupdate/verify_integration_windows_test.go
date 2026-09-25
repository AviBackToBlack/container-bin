//go:build windows

package selfupdate

import (
	"context"
	"os"
	"testing"
)

func TestVerifyRealGitHubCLIRelease(t *testing.T) {
	gh := os.Getenv("CB_TEST_REAL_GH")
	binary := os.Getenv("CB_TEST_RELEASE_CB")
	checksums := os.Getenv("CB_TEST_RELEASE_SUMS")
	installed := os.Getenv("CB_TEST_INSTALLED_CB")
	if gh == "" || binary == "" || checksums == "" || installed == "" {
		t.Skip("set CB_TEST_REAL_GH, CB_TEST_RELEASE_CB, CB_TEST_RELEASE_SUMS and CB_TEST_INSTALLED_CB to run the live release verification")
	}
	binaryInfo, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}
	checksumsInfo, err := os.Stat(checksums)
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan{
		Current:      "v1.0.0",
		Target:       "v1.1.0",
		OS:           "windows",
		Arch:         "amd64",
		ReleaseURL:   releaseWebRoot + "/tag/v1.1.0",
		Binary:       Asset{Name: "cb.exe", URL: releaseWebRoot + "/download/v1.1.0/cb.exe", Size: binaryInfo.Size()},
		Archive:      Asset{Name: "container-bin-v1.1.0-windows-amd64.zip", URL: releaseWebRoot + "/download/v1.1.0/container-bin-v1.1.0-windows-amd64.zip", Size: 1},
		Checksums:    Asset{Name: "SHA256SUMS", URL: releaseWebRoot + "/download/v1.1.0/SHA256SUMS", Size: checksumsInfo.Size()},
		ExpectedRepo: expectedReleaseRepo,
		ExpectedRef:  "refs/tags/v1.1.0",
		Workflow:     expectedReleaseWorkflow,
	}
	verified, err := Verify(context.Background(), plan, binary, checksums, installed, gh)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Target() != "v1.1.0" || verified.Size() != binaryInfo.Size() || len(verified.SHA256()) != 64 {
		t.Fatalf("unexpected live verification result: %+v", verified)
	}
}
