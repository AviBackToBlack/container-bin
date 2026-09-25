package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStageDownloadsPrivateExactAssetsBesideDestination(t *testing.T) {
	plan := stagingPlan()
	binary := bytes.Repeat([]byte("b"), int(plan.Binary.Size))
	checksums := bytes.Repeat([]byte("s"), int(plan.Checksums.Size))
	requests := 0
	doer := doerFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.Method != http.MethodGet || req.Header.Get("Accept") != "application/octet-stream" || req.Header.Get("Accept-Encoding") != "identity" || req.Header.Get("X-GitHub-Api-Version") != apiVersion || req.Header.Get("User-Agent") != "container-bin/v1.1.0" {
			t.Fatalf("unexpected download request: method=%s headers=%v", req.Method, req.Header)
		}
		switch req.URL.String() {
		case plan.Checksums.URL:
			return assetResponse(req, checksums), nil
		case plan.Binary.URL:
			return assetResponse(req, binary), nil
		default:
			t.Fatalf("unexpected download URL: %s", req.URL)
			return nil, nil
		}
	})
	destination, installed := testInstallation(t)
	staged, err := (stager{doer: doer}).Stage(context.Background(), plan, installed)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = staged.Cleanup() })
	if requests != 2 || staged.Target != plan.Target {
		t.Fatalf("unexpected staged result: requests=%d staged=%+v", requests, staged)
	}
	resolvedDestination, err := filepath.EvalSymlinks(destination)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(staged.Dir) != resolvedDestination || !strings.HasPrefix(filepath.Base(staged.Dir), stagingPrefix) {
		t.Fatalf("staging directory %q is not directly under destination %q", staged.Dir, resolvedDestination)
	}
	assertFile(t, staged.BinaryPath, binary)
	assertFile(t, staged.ChecksumsPath, checksums)
	if runtime.GOOS != "windows" { // Windows FileMode does not report ACL permissions.
		if info, err := os.Stat(staged.Dir); err != nil || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("staging directory permissions = %v, %v", info, err)
		}
		for _, path := range []string{staged.BinaryPath, staged.ChecksumsPath} {
			if info, err := os.Stat(path); err != nil || info.Mode().Perm()&0o177 != 0 {
				t.Fatalf("staged file %q permissions = %v, %v", path, info, err)
			}
		}
	}
	if err := staged.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staged.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Cleanup left staging directory: %v", err)
	}
}

func TestStagedCleanupRejectsUnrelatedPaths(t *testing.T) {
	unrelated := t.TempDir()
	staged := Staged{
		Dir:           unrelated,
		BinaryPath:    filepath.Join(unrelated, "cb.exe"),
		ChecksumsPath: filepath.Join(unrelated, "SHA256SUMS"),
	}
	if err := staged.Cleanup(); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("Cleanup error = %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("Cleanup touched unrelated directory: %v", err)
	}
}

func TestStageDownloadsARM64ArchiveSelectedByPlan(t *testing.T) {
	plan := arm64StagingPlan()
	archive := bytes.Repeat([]byte("a"), int(plan.Binary.Size))
	checksums := bytes.Repeat([]byte("s"), int(plan.Checksums.Size))
	doer := doerFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case plan.Checksums.URL:
			return assetResponse(req, checksums), nil
		case plan.Binary.URL:
			return assetResponse(req, archive), nil
		default:
			t.Fatalf("unexpected download URL: %s", req.URL)
			return nil, nil
		}
	})
	_, installed := testInstallation(t)
	staged, err := (stager{doer: doer}).Stage(context.Background(), plan, installed)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(staged.BinaryPath) != plan.Binary.Name {
		t.Fatalf("staged artifact = %q, want %q", staged.BinaryPath, plan.Binary.Name)
	}
	assertFile(t, staged.BinaryPath, archive)
	if err := staged.Cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestStageCleansUpEveryPartialFailure(t *testing.T) {
	cases := []struct {
		name string
		doer func(Plan) httpDoer
		want string
	}{
		{
			name: "network",
			doer: func(Plan) httpDoer {
				return doerFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })
			},
			want: "offline",
		},
		{
			name: "HTTP status",
			doer: func(Plan) httpDoer {
				return doerFunc(func(req *http.Request) (*http.Response, error) {
					return assetStatusResponse(req, http.StatusBadGateway, nil, -1), nil
				})
			},
			want: "HTTP 502",
		},
		{
			name: "truncated checksum",
			doer: func(plan Plan) httpDoer {
				return doerFunc(func(req *http.Request) (*http.Response, error) {
					return assetStatusResponse(req, http.StatusOK, make([]byte, plan.Checksums.Size-1), plan.Checksums.Size), nil
				})
			},
			want: "was truncated",
		},
		{
			name: "oversized checksum",
			doer: func(plan Plan) httpDoer {
				return doerFunc(func(req *http.Request) (*http.Response, error) {
					return assetStatusResponse(req, http.StatusOK, make([]byte, plan.Checksums.Size+1), -1), nil
				})
			},
			want: "exceeded its advertised size",
		},
		{
			name: "network loss after manifest",
			doer: func(plan Plan) httpDoer {
				return doerFunc(func(req *http.Request) (*http.Response, error) {
					if req.URL.String() == plan.Checksums.URL {
						return assetResponse(req, make([]byte, plan.Checksums.Size)), nil
					}
					return nil, errors.New("connection reset")
				})
			},
			want: "connection reset",
		},
		{
			name: "network loss while reading body",
			doer: func(plan Plan) httpDoer {
				return doerFunc(func(req *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode:    http.StatusOK,
						Body:          io.NopCloser(&errorReader{data: []byte("partial"), err: errors.New("connection lost")}),
						Header:        make(http.Header),
						ContentLength: plan.Checksums.Size,
						Request:       req,
					}, nil
				})
			},
			want: "connection lost",
		},
		{
			name: "unexpected partial response",
			doer: func(plan Plan) httpDoer {
				return doerFunc(func(req *http.Request) (*http.Response, error) {
					resp := assetResponse(req, make([]byte, plan.Checksums.Size))
					resp.Header.Set("Content-Range", "bytes 0-31/64")
					return resp, nil
				})
			},
			want: "partial response",
		},
		{
			name: "external final URL",
			doer: func(plan Plan) httpDoer {
				return doerFunc(func(req *http.Request) (*http.Response, error) {
					resp := assetResponse(req, make([]byte, plan.Checksums.Size))
					resp.Request.URL, _ = url.Parse("https://evil.example/asset")
					return resp, nil
				})
			},
			want: "outside the permitted GitHub asset flow",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := stagingPlan()
			destination, installed := testInstallation(t)
			_, err := (stager{doer: tc.doer(plan)}).Stage(context.Background(), plan, installed)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Stage error = %v, want %q", err, tc.want)
			}
			entries, readErr := stagingEntries(destination)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("failed staging left files behind: entries=%v err=%v", entries, readErr)
			}
		})
	}
}

func TestStageRejectsInvalidInputsBeforeCreatingFilesOrCallingNetwork(t *testing.T) {
	base := stagingPlan()
	cases := []struct {
		name   string
		mutate func(*Plan)
		want   string
	}{
		{name: "development current", mutate: func(p *Plan) { p.Current = "dev" }, want: "development builds"},
		{name: "already installed", mutate: func(p *Plan) { p.Target = p.Current }, want: "already installed"},
		{name: "unauthorized downgrade", mutate: func(p *Plan) { p.Target = "v1.0.0" }, want: "does not authorize"},
		{name: "inconsistent downgrade authorization", mutate: func(p *Plan) {
			p.downgradeAuthorization = downgradeAuthorization{current: p.Current, target: "v1.0.0"}
		}, want: "inconsistent"},
		{name: "wrong platform", mutate: func(p *Plan) { p.Arch = "386" }, want: "no qualified artifact"},
		{name: "wrong release", mutate: func(p *Plan) { p.ReleaseURL = "https://evil.example/release" }, want: "non-canonical release URL"},
		{name: "wrong provenance", mutate: func(p *Plan) { p.ExpectedRepo = "other/repo" }, want: "unexpected provenance policy"},
		{name: "wrong binary name", mutate: func(p *Plan) { p.Binary.Name = "other.exe" }, want: "expected asset"},
		{name: "wrong binary URL", mutate: func(p *Plan) { p.Binary.URL = "https://evil.example/cb.exe" }, want: "non-canonical download URL"},
		{name: "oversized manifest", mutate: func(p *Plan) { p.Checksums.Size = maxChecksumSize + 1 }, want: "invalid size"},
		{name: "wrong archive layout", mutate: func(p *Plan) { p.Archive.Name = "other.zip" }, want: "expected asset"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := base
			tc.mutate(&plan)
			called := false
			destination, installed := testInstallation(t)
			_, err := (stager{doer: doerFunc(func(*http.Request) (*http.Response, error) { called = true; return nil, nil })}).Stage(context.Background(), plan, installed)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Stage error = %v, want %q", err, tc.want)
			}
			if called {
				t.Fatal("network called for rejected plan")
			}
			entries, _ := stagingEntries(destination)
			if len(entries) != 0 {
				t.Fatalf("invalid plan created staging files: %v", entries)
			}
		})
	}

	t.Run("relative installed executable", func(t *testing.T) {
		called := false
		_, err := (stager{doer: doerFunc(func(*http.Request) (*http.Response, error) { called = true; return nil, nil })}).Stage(context.Background(), base, "relative")
		if err == nil || !strings.Contains(err.Error(), "must be absolute") || called {
			t.Fatalf("relative executable result = %v, called=%v", err, called)
		}
	})

	t.Run("installed executable is a directory", func(t *testing.T) {
		path := t.TempDir()
		_, err := (stager{doer: doerFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("unexpected") })}).Stage(context.Background(), base, path)
		if err == nil || !strings.Contains(err.Error(), "regular non-symlink file") {
			t.Fatalf("directory executable error = %v", err)
		}
	})
}

func TestStageAcceptsOnlyAuthorizedDowngradePlan(t *testing.T) {
	plan := retargetStagingPlan(stagingPlan(), "v1.0.0")
	if err := validateStagingPlan(plan); err == nil || !strings.Contains(err.Error(), "does not authorize") {
		t.Fatalf("unauthorized downgrade validation = %v", err)
	}
	plan.downgradeAuthorization = downgradeAuthorization{current: plan.Current, target: plan.Target}
	if err := validateStagingPlan(plan); err != nil {
		t.Fatalf("authorized downgrade validation = %v", err)
	}
	plan.Target = "v0.9.0"
	if err := validateStagingPlan(plan); err == nil || !strings.Contains(err.Error(), "does not authorize") {
		t.Fatalf("mutated authorized downgrade validation = %v", err)
	}
}

func TestStageCleansUpRestrictionFailureAndReportsCleanupFailure(t *testing.T) {
	destination, installed := testInstallation(t)
	restrictErr := errors.New("ACL unavailable")
	_, err := (stager{
		doer:         doerFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("unexpected network") }),
		restrictPath: func(string, bool) error { return restrictErr },
	}).Stage(context.Background(), stagingPlan(), installed)
	if !errors.Is(err, restrictErr) {
		t.Fatalf("restriction failure = %v", err)
	}
	entries, readErr := stagingEntries(destination)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("restriction failure left staging files: entries=%v err=%v", entries, readErr)
	}

	cleanupErr := errors.New("cleanup unavailable")
	_, err = (stager{
		doer:         doerFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("unexpected network") }),
		restrictPath: func(string, bool) error { return restrictErr },
		removeAll:    func(string) error { return cleanupErr },
	}).Stage(context.Background(), stagingPlan(), installed)
	if !errors.Is(err, restrictErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("combined restriction/cleanup failure = %v", err)
	}
	entries, _ = stagingEntries(destination)
	for _, entry := range entries {
		_ = os.RemoveAll(filepath.Join(destination, entry.Name()))
	}
}

func TestDownloadAcceptsOnlyCanonicalOrSingleGitHubAssetRedirect(t *testing.T) {
	canonical, _ := url.Parse("https://github.com/AviBackToBlack/container-bin/releases/download/v1.2.0/cb.exe")
	cdn, _ := url.Parse("https://release-assets.githubusercontent.com/github-production-release-asset/123/abc?sig=value")
	if !isCanonicalReleaseAssetURL(canonical) || !isReleaseAssetCDNURL(cdn) || !isPermittedFinalURL(canonical, canonical.String()) || !isPermittedFinalURL(cdn, canonical.String()) {
		t.Fatal("expected canonical GitHub asset flow to be accepted")
	}
	for _, raw := range []string{
		"http://github.com/AviBackToBlack/container-bin/releases/download/v1.2.0/cb.exe",
		"https://github.com/other/repo/releases/download/v1.2.0/cb.exe",
		"https://release-assets.githubusercontent.com/other/123?sig=value",
		"https://release-assets.githubusercontent.com/github-production-release-asset/123/abc",
		"https://release-assets.githubusercontent.com.evil.example/github-production-release-asset/123/abc?sig=value",
	} {
		candidate, _ := url.Parse(raw)
		if isPermittedFinalURL(candidate, canonical.String()) {
			t.Errorf("unexpectedly permitted final URL %q", raw)
		}
	}

	client := newDownloadClient()
	original := &http.Request{URL: canonical, Header: make(http.Header)}
	if err := client.CheckRedirect(&http.Request{URL: cdn, Header: make(http.Header)}, []*http.Request{original}); err != nil {
		t.Fatalf("allowed redirect rejected: %v", err)
	}
	external, _ := url.Parse("https://evil.example/asset")
	if err := client.CheckRedirect(&http.Request{URL: external, Header: make(http.Header)}, []*http.Request{original}); err == nil {
		t.Fatal("external redirect accepted")
	}
	if err := client.CheckRedirect(&http.Request{URL: cdn, Header: make(http.Header)}, []*http.Request{original, {URL: cdn}}); err == nil {
		t.Fatal("second redirect accepted")
	}
}

func stagingPlan() Plan {
	return Plan{
		Current:        "v1.1.0",
		Target:         "v1.2.0",
		Channel:        "stable",
		Status:         "UPDATE AVAILABLE",
		OS:             "windows",
		Arch:           "amd64",
		ReleaseURL:     releaseWebRoot + "/tag/v1.2.0",
		Binary:         Asset{Name: "cb.exe", URL: releaseWebRoot + "/download/v1.2.0/cb.exe", Size: 16},
		Archive:        Asset{Name: "container-bin-v1.2.0-windows-amd64.zip", URL: releaseWebRoot + "/download/v1.2.0/container-bin-v1.2.0-windows-amd64.zip", Size: 32},
		Checksums:      Asset{Name: "SHA256SUMS", URL: releaseWebRoot + "/download/v1.2.0/SHA256SUMS", Size: 64},
		ExpectedRepo:   "AviBackToBlack/container-bin",
		ExpectedRef:    "refs/tags/v1.2.0",
		Workflow:       ".github/workflows/release.yml",
		checksumLayout: checksumLayoutLegacyAMD64,
	}
}

func arm64StagingPlan() Plan {
	plan := stagingPlan()
	plan.Arch = "arm64"
	name := "container-bin-v1.2.0-windows-arm64.zip"
	asset := Asset{Name: name, URL: releaseWebRoot + "/download/v1.2.0/" + name, Size: 32}
	plan.Binary = asset
	plan.Archive = asset
	plan.checksumLayout = checksumLayoutDualArch
	return plan
}

func retargetStagingPlan(plan Plan, target string) Plan {
	plan.Target = target
	plan.ReleaseURL = releaseWebRoot + "/tag/" + target
	plan.Binary.URL = releaseWebRoot + "/download/" + target + "/cb.exe"
	plan.Archive.Name = "container-bin-" + target + "-windows-amd64.zip"
	plan.Archive.URL = releaseWebRoot + "/download/" + target + "/" + plan.Archive.Name
	plan.Checksums.URL = releaseWebRoot + "/download/" + target + "/SHA256SUMS"
	plan.ExpectedRef = "refs/tags/" + target
	return plan
}

func testInstallation(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	executable := filepath.Join(dir, "cb.exe")
	if err := os.WriteFile(executable, []byte("installed ContainerBin test executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir, executable
}

func stagingEntries(destination string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(destination)
	if err != nil {
		return nil, err
	}
	filtered := entries[:0]
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), stagingPrefix) {
			filtered = append(filtered, entry)
		}
	}
	return filtered, nil
}

func assetResponse(req *http.Request, body []byte) *http.Response {
	return assetStatusResponse(req, http.StatusOK, body, int64(len(body)))
}

func assetStatusResponse(req *http.Request, status int, body []byte, contentLength int64) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Body:          io.NopCloser(bytes.NewReader(body)),
		Header:        make(http.Header),
		ContentLength: contentLength,
		Request:       req,
	}
}

func assertFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s contents differ", path)
	}
}

type errorReader struct {
	data []byte
	err  error
}

func (r *errorReader) Read(p []byte) (int, error) {
	if len(r.data) != 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	return 0, r.err
}
