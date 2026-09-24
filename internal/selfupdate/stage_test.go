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
	destination := t.TempDir()
	staged, err := (stager{doer: doer}).Stage(context.Background(), plan, destination)
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
			destination := t.TempDir()
			_, err := (stager{doer: tc.doer(plan)}).Stage(context.Background(), plan, destination)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Stage error = %v, want %q", err, tc.want)
			}
			entries, readErr := os.ReadDir(destination)
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
		{name: "wrong platform", mutate: func(p *Plan) { p.Arch = "arm64" }, want: "no qualified artifact"},
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
			destination := t.TempDir()
			_, err := (stager{doer: doerFunc(func(*http.Request) (*http.Response, error) { called = true; return nil, nil })}).Stage(context.Background(), plan, destination)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Stage error = %v, want %q", err, tc.want)
			}
			if called {
				t.Fatal("network called for rejected plan")
			}
			entries, _ := os.ReadDir(destination)
			if len(entries) != 0 {
				t.Fatalf("invalid plan created staging files: %v", entries)
			}
		})
	}

	t.Run("relative destination", func(t *testing.T) {
		called := false
		_, err := (stager{doer: doerFunc(func(*http.Request) (*http.Response, error) { called = true; return nil, nil })}).Stage(context.Background(), base, "relative")
		if err == nil || !strings.Contains(err.Error(), "must be absolute") || called {
			t.Fatalf("relative destination result = %v, called=%v", err, called)
		}
	})

	t.Run("destination is a file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "cb.exe")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := (stager{doer: doerFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("unexpected") })}).Stage(context.Background(), base, path)
		if err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("file destination error = %v", err)
		}
	})
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
		Current:      "v1.1.0",
		Target:       "v1.2.0",
		Channel:      "stable",
		Status:       "UPDATE AVAILABLE",
		OS:           "windows",
		Arch:         "amd64",
		ReleaseURL:   releaseWebRoot + "/tag/v1.2.0",
		Binary:       Asset{Name: "cb.exe", URL: releaseWebRoot + "/download/v1.2.0/cb.exe", Size: 16},
		Archive:      Asset{Name: "container-bin-v1.2.0-windows-amd64.zip", URL: releaseWebRoot + "/download/v1.2.0/container-bin-v1.2.0-windows-amd64.zip", Size: 32},
		Checksums:    Asset{Name: "SHA256SUMS", URL: releaseWebRoot + "/download/v1.2.0/SHA256SUMS", Size: 64},
		ExpectedRepo: "AviBackToBlack/container-bin",
		ExpectedRef:  "refs/tags/v1.2.0",
		Workflow:     ".github/workflows/release.yml",
	}
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
