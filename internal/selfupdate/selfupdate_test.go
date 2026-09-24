package selfupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

func TestParseArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    Options
		wantErr string
	}{
		{name: "stable", args: []string{"--check"}, want: Options{Check: true}},
		{name: "prerelease", args: []string{"--check", "--prerelease"}, want: Options{Check: true, Prerelease: true}},
		{name: "exact downgrade", args: []string{"--version", "v1.0.0", "--allow-downgrade", "--check"}, want: Options{Check: true, Version: "v1.0.0", AllowDowngrade: true}},
		{name: "check required", wantErr: "only the read-only selection phase"},
		{name: "mutually exclusive", args: []string{"--check", "--prerelease", "--version", "v1.0.0"}, wantErr: "mutually exclusive"},
		{name: "bad exact", args: []string{"--check", "--version", "latest"}, wantErr: "canonical"},
		{name: "missing exact", args: []string{"--check", "--version"}, wantErr: "requires"},
		{name: "empty exact", args: []string{"--check", "--version", ""}, wantErr: "requires"},
		{name: "missing exact before flag", args: []string{"--check", "--version", "--prerelease"}, wantErr: "requires"},
		{name: "unknown", args: []string{"--check", "--apply"}, wantErr: "unknown"},
		{name: "duplicate check", args: []string{"--check", "--check"}, wantErr: "only once"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseArgs(tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ParseArgs(%v) error = %v, want %q", tc.args, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("ParseArgs(%v) = (%+v, %v), want %+v", tc.args, got, err, tc.want)
			}
		})
	}
}

func TestVersionParsingAndPrecedence(t *testing.T) {
	ordered := []string{
		"v1.0.0-alpha",
		"v1.0.0-alpha.1",
		"v1.0.0-alpha-beta",
		"v1.0.0-beta",
		"v1.0.0-beta.2",
		"v1.0.0-beta.11",
		"v1.0.0-beta.184467440737095516160",
		"v1.0.0-rc.1",
		"v1.0.0",
		"v1.0.1",
		"v2.0.0",
	}
	for i, raw := range ordered {
		v, err := parseVersion(raw)
		if err != nil {
			t.Fatalf("parseVersion(%q): %v", raw, err)
		}
		if i > 0 {
			previous, _ := parseVersion(ordered[i-1])
			if previous.compare(v) >= 0 || v.compare(previous) <= 0 {
				t.Fatalf("version order not strict: %s, %s", previous.raw, v.raw)
			}
		}
	}
	for _, raw := range []string{"dev", "1.2.3", "v1.2", "v01.2.3", "v1.2.3-01", "v1.2.3+meta", "v1.2.3-"} {
		if _, err := parseVersion(raw); err == nil {
			t.Errorf("parseVersion(%q) succeeded", raw)
		}
	}
}

func TestPlanStableSelectionAndHeaders(t *testing.T) {
	selected := canonicalRelease("v1.2.0", false)
	c := checker{doer: doerFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != apiRoot+"/releases/latest" {
			t.Fatalf("request URL = %s", req.URL)
		}
		if req.Header.Get("Accept") != "application/vnd.github+json" || req.Header.Get("X-GitHub-Api-Version") != apiVersion || req.Header.Get("User-Agent") != "container-bin/v1.1.0" {
			t.Fatalf("unexpected request headers: %v", req.Header)
		}
		return jsonResponse(t, selected), nil
	})}
	plan, err := c.Plan(context.Background(), "v1.1.0", "windows", "amd64", Options{Check: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Target != "v1.2.0" || plan.Channel != "stable" || plan.Status != "UPDATE AVAILABLE" {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if plan.Archive.Name != "container-bin-v1.2.0-windows-amd64.zip" || plan.Binary.Name != "cb.exe" || plan.Checksums.Name != "SHA256SUMS" {
		t.Fatalf("unexpected assets: %+v", plan)
	}
	if plan.ExpectedRepo != "AviBackToBlack/container-bin" || plan.ExpectedRef != "refs/tags/v1.2.0" || plan.Workflow != ".github/workflows/release.yml" {
		t.Fatalf("unexpected verification policy: %+v", plan)
	}
}

func TestPlanExactAndDowngradePolicy(t *testing.T) {
	selected := canonicalRelease("v1.0.0", false)
	c := checker{doer: releaseDoer(t, apiRoot+"/releases/tags/v1.0.0", selected)}
	_, err := c.Plan(context.Background(), "v1.1.0", "windows", "amd64", Options{Check: true, Version: "v1.0.0"})
	if err == nil || !strings.Contains(err.Error(), "--allow-downgrade") {
		t.Fatalf("downgrade error = %v", err)
	}
	plan, err := c.Plan(context.Background(), "v1.1.0", "windows", "amd64", Options{Check: true, Version: "v1.0.0", AllowDowngrade: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Channel != "exact" || plan.Status != "DOWNGRADE AUTHORIZED (CHECK ONLY)" {
		t.Fatalf("unexpected exact downgrade plan: %+v", plan)
	}
}

func TestPlanCurrent(t *testing.T) {
	selected := canonicalRelease("v1.1.0", false)
	c := checker{doer: releaseDoer(t, apiRoot+"/releases/latest", selected)}
	plan, err := c.Plan(context.Background(), "v1.1.0", "windows", "amd64", Options{Check: true})
	if err != nil || plan.Status != "CURRENT" {
		t.Fatalf("current plan = (%+v, %v)", plan, err)
	}
}

func TestPlanPrereleaseSelectsHighestCanonicalPublishedCandidate(t *testing.T) {
	releases := []release{
		canonicalRelease("v1.3.0-rc.1", true),
		canonicalRelease("v1.2.0", false),
		canonicalRelease("not-semver", true),
		canonicalRelease("v1.3.0-beta.2", true),
		canonicalRelease("v2.0.0-rc.1", true),
	}
	releases[4].Draft = true
	c := checker{doer: releaseDoer(t, apiRoot+"/releases?per_page=30", releases)}
	plan, err := c.Plan(context.Background(), "v1.1.0", "windows", "amd64", Options{Check: true, Prerelease: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Target != "v1.3.0-rc.1" || plan.Channel != "prerelease" {
		t.Fatalf("unexpected prerelease plan: %+v", plan)
	}
}

func TestPlanRejectsDevelopmentAndUnsupportedPlatformBeforeNetwork(t *testing.T) {
	called := false
	c := checker{doer: doerFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("unexpected")
	})}
	for _, tc := range []struct {
		current, goos, goarch, want string
	}{
		{"dev", "windows", "amd64", "development builds"},
		{"v0.0.0-dev.abc", "windows", "amd64", "development builds"},
		{"v1.1.0", "windows", "arm64", "no qualified artifact"},
		{"v1.1.0", "linux", "amd64", "no qualified artifact"},
	} {
		_, err := c.Plan(context.Background(), tc.current, tc.goos, tc.goarch, Options{Check: true})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Plan(%q,%s/%s) error = %v, want %q", tc.current, tc.goos, tc.goarch, err, tc.want)
		}
	}
	if called {
		t.Fatal("network called for locally rejected plan")
	}
}

func TestPlanRejectsUnsafeReleaseMetadata(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*release)
		want   string
	}{
		{name: "missing", mutate: func(r *release) { r.Assets = r.Assets[:2] }, want: "missing required asset"},
		{name: "duplicate", mutate: func(r *release) { r.Assets = append(r.Assets, r.Assets[0]) }, want: "duplicate required asset"},
		{name: "external asset URL", mutate: func(r *release) { r.Assets[0].BrowserDownloadURL = "https://evil.example/cb.exe" }, want: "non-canonical download URL"},
		{name: "external release URL", mutate: func(r *release) { r.HTMLURL = "https://evil.example/v1.2.0" }, want: "outside the canonical release page"},
		{name: "oversize", mutate: func(r *release) { r.Assets[0].Size = maxBinarySize + 1 }, want: "invalid size"},
		{name: "prerelease from stable", mutate: func(r *release) { r.Prerelease = true }, want: "prerelease"},
		{name: "prerelease tag without flag", mutate: func(r *release) { r.TagName = "v1.2.0-rc.1" }, want: "prerelease"},
		{name: "invalid stable tag", mutate: func(r *release) { r.TagName = "latest" }, want: "invalid tag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			selected := canonicalRelease("v1.2.0", false)
			tc.mutate(&selected)
			c := checker{doer: releaseDoer(t, apiRoot+"/releases/latest", selected)}
			_, err := c.Plan(context.Background(), "v1.1.0", "windows", "amd64", Options{Check: true})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unsafe metadata error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestGetJSONBoundsAndTransportFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		doer httpDoer
		want string
	}{
		{name: "network", doer: doerFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") }), want: "offline"},
		{name: "http", doer: doerFunc(func(*http.Request) (*http.Response, error) { return response(http.StatusForbidden, "{}"), nil }), want: "/repos/AviBackToBlack/container-bin/releases/latest returned HTTP 403"},
		{name: "truncated JSON", doer: doerFunc(func(*http.Request) (*http.Response, error) { return response(http.StatusOK, "{"), nil }), want: "decode"},
		{name: "oversize", doer: doerFunc(func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, strings.Repeat("x", maxResponseSize+1)), nil
		}), want: "1 MiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dst release
			err := (checker{doer: tc.doer}).getJSON(context.Background(), apiRoot+"/releases/latest", "v1.0.0", &dst)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("getJSON error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestPrintPlanStatesReadOnlyBoundary(t *testing.T) {
	selected := canonicalRelease("v1.2.0", false)
	c := checker{doer: releaseDoer(t, apiRoot+"/releases/latest", selected)}
	plan, err := c.Plan(context.Background(), "v1.1.0", "windows", "amd64", Options{Check: true})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	printPlan(&out, plan)
	for _, want := range []string{"read-only; no files changed", "UPDATE AVAILABLE", "container-bin-v1.2.0-windows-amd64.zip", "gh attestation verify", "no fallback", "apply:        unavailable"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("plan output missing %q:\n%s", want, out.String())
		}
	}
}

func canonicalRelease(tag string, prerelease bool) release {
	archive := "container-bin-" + tag + "-windows-amd64.zip"
	asset := func(name string, size int64) releaseAsset {
		return releaseAsset{Name: name, Size: size, BrowserDownloadURL: releaseWebRoot + "/download/" + tag + "/" + name}
	}
	return release{
		TagName:    tag,
		HTMLURL:    releaseWebRoot + "/tag/" + tag,
		Prerelease: prerelease,
		Assets: []releaseAsset{
			asset("cb.exe", 3<<20),
			asset(archive, 2<<20),
			asset("SHA256SUMS", 178),
		},
	}
}

func releaseDoer(t *testing.T, wantURL string, value any) httpDoer {
	t.Helper()
	return doerFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != wantURL {
			t.Fatalf("request URL = %s, want %s", req.URL, wantURL)
		}
		return jsonResponse(t, value), nil
	})
}

func jsonResponse(t *testing.T, value any) *http.Response {
	t.Helper()
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(value); err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(b.Bytes())), Header: make(http.Header)}
}

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
