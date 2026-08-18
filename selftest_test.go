package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBuildSelfTestReport(t *testing.T) {
	fixedTime := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	version := "v0.5.0"
	passDocker := selfTestCheck{ID: "docker", Status: "pass", Message: "docker 27.0.0"}
	failDocker := selfTestCheck{ID: "docker", Status: "fail", Message: "docker unavailable"}
	allPass := map[string]toolSelfTestOutcome{
		"python":    {},
		"node":      {},
		"jq":        {},
		"terraform": {},
	}
	wantIDs := []string{
		"docker",
		"python-image-local",
		"python-persist-write",
		"python-persist-read",
		"python-external-path",
		"node-image-local",
		"node-modules-write",
		"node-modules-read",
		"jq-image-local",
		"jq-relative-path",
		"terraform-image-local",
		"terraform-chdir",
	}

	cases := []struct {
		name         string
		docker       selfTestCheck
		outcomes     map[string]toolSelfTestOutcome
		environment  []selfTestCheck
		wantOK       bool
		wantPassed   int
		wantFailed   int
		wantSkipped  int
		wantStatus   map[string]string
		wantContains map[string]string
	}{
		{
			name:        "all_pass",
			docker:      passDocker,
			outcomes:    allPass,
			wantOK:      true,
			wantPassed:  12,
			wantFailed:  0,
			wantSkipped: 0,
			wantStatus: map[string]string{
				"docker":                "pass",
				"python-image-local":    "pass",
				"python-persist-write":  "pass",
				"python-persist-read":   "pass",
				"python-external-path":  "pass",
				"node-image-local":      "pass",
				"node-modules-write":    "pass",
				"node-modules-read":     "pass",
				"jq-image-local":        "pass",
				"jq-relative-path":      "pass",
				"terraform-image-local": "pass",
				"terraform-chdir":       "pass",
			},
		},
		{
			name:        "docker_fails",
			docker:      failDocker,
			outcomes:    map[string]toolSelfTestOutcome{},
			wantOK:      false,
			wantPassed:  0,
			wantFailed:  1,
			wantSkipped: 11,
			wantContains: map[string]string{
				"python-image-local": "docker",
				"jq-relative-path":   "docker",
			},
		},
		{
			name:   "python_image_local_fails_others_pass",
			docker: passDocker,
			outcomes: map[string]toolSelfTestOutcome{
				"python":    {ImageLocalErr: ptr("required image is not local: python:3.13 (run `cb lock`/`cb update` first)")},
				"node":      {},
				"jq":        {},
				"terraform": {},
			},
			wantOK:      false,
			wantPassed:  8, // docker + node(3) + jq(2) + terraform(2)
			wantFailed:  1, // python-image-local
			wantSkipped: 3, // python persist-write/read/external-path
			wantStatus: map[string]string{
				"python-image-local":   "fail",
				"python-persist-write": "skip",
				"python-persist-read":  "skip",
				"python-external-path": "skip",
				"node-image-local":     "pass",
				"jq-image-local":       "pass",
			},
			wantContains: map[string]string{
				"python-persist-write": "python-image-local",
			},
		},
		{
			name:   "python_persist_write_fails",
			docker: passDocker,
			outcomes: map[string]toolSelfTestOutcome{
				"python":    {PersistWriteErr: ptr("python exited 1")},
				"node":      {},
				"jq":        {},
				"terraform": {},
			},
			wantOK:      false,
			wantPassed:  10, // docker + python image-local + external-path + node(3) + jq(2) + terraform(2)
			wantFailed:  1,
			wantSkipped: 1, // python-persist-read
			wantStatus: map[string]string{
				"python-image-local":   "pass",
				"python-persist-write": "fail",
				"python-persist-read":  "skip",
				"python-external-path": "pass",
				"node-image-local":     "pass",
			},
			wantContains: map[string]string{
				"python-persist-read": "python-persist-write",
			},
		},
		{
			// A tool missing from the registry must NOT read as an overall
			// pass: the pre-refactor self-test failed outright the moment it
			// reached a missing tool ("tool %s missing"), and this report
			// must preserve that fail-closed guarantee even though the
			// per-check reporting is now far more granular. See the Devin
			// Review finding on PR #21 that caught this.
			name:   "node_not_registered",
			docker: passDocker,
			outcomes: map[string]toolSelfTestOutcome{
				"python":    {},
				"jq":        {},
				"terraform": {},
			},
			wantOK:      false,
			wantPassed:  9, // docker + python(4) + jq(2) + terraform(2)
			wantFailed:  1, // node-image-local
			wantSkipped: 2, // node-modules-write, node-modules-read
			wantStatus: map[string]string{
				"node-image-local":   "fail",
				"node-modules-write": "skip",
				"node-modules-read":  "skip",
			},
			wantContains: map[string]string{
				"node-image-local":   "not registered",
				"node-modules-write": "node-image-local",
				"node-modules-read":  "node-image-local",
			},
		},
		{
			name:        "metadata_from_params",
			docker:      passDocker,
			outcomes:    allPass,
			wantOK:      true,
			wantPassed:  12,
			wantFailed:  0,
			wantSkipped: 0,
		},
		{
			name:   "counts_mixed",
			docker: passDocker,
			outcomes: map[string]toolSelfTestOutcome{
				"python": {ImageLocalErr: ptr("image missing")},
				// node absent => node-image-local fails, its 2 dependents skip
				"jq":        {},
				"terraform": {ChdirErr: ptr("terraform exited 1")},
			},
			wantOK:      false,
			wantPassed:  4, // docker + jq(2) + terraform-image-local
			wantFailed:  3, // python-image-local, node-image-local, terraform-chdir
			wantSkipped: 5, // python persist(3) + node-modules(2)
			wantStatus: map[string]string{
				"node-image-local": "fail",
			},
		},
		{
			name:     "environment_does_not_affect_tally",
			docker:   passDocker,
			outcomes: allPass,
			environment: []selfTestCheck{
				{ID: "windows-version", Status: "pass", Message: "Microsoft Windows NT 10.0.26200.0"},
				{ID: "powershell-version", Status: "pass", Message: "5.1.22621.3672"},
				{ID: "docker-engine-version", Status: "pass", Message: "docker 27.0.0"},
				{ID: "docker-os-type", Status: "fail", Message: "docker is in Windows-container mode; ContainerBin runs Linux images only — switch to Linux containers from the Docker Desktop tray menu"},
				{ID: "cwd-reparse-point", Status: "pass", Message: "current directory does not sit behind a reparse point"},
				{ID: "shim-dir-network-storage", Status: "skip", Message: "shim directory is on a Fixed drive (C:\\Tools)"},
				{ID: "cwd-network-storage", Status: "pass", Message: "current directory is on a Fixed drive (C:\\Work)"},
			},
			wantOK:      true,
			wantPassed:  12,
			wantFailed:  0,
			wantSkipped: 0,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := buildSelfTestReport(version, fixedTime, c.docker, c.outcomes, c.environment)

			if r.OK != c.wantOK {
				t.Errorf("OK = %v, want %v", r.OK, c.wantOK)
			}
			if r.Passed != c.wantPassed {
				t.Errorf("Passed = %d, want %d", r.Passed, c.wantPassed)
			}
			if r.Failed != c.wantFailed {
				t.Errorf("Failed = %d, want %d", r.Failed, c.wantFailed)
			}
			if r.Skipped != c.wantSkipped {
				t.Errorf("Skipped = %d, want %d", r.Skipped, c.wantSkipped)
			}

			if len(r.Checks) != len(wantIDs) {
				t.Fatalf("len(Checks) = %d, want %d", len(r.Checks), len(wantIDs))
			}
			for i, id := range wantIDs {
				if r.Checks[i].ID != id {
					t.Fatalf("Checks[%d].ID = %q, want %q", i, r.Checks[i].ID, id)
				}
			}

			if !reflect.DeepEqual(r.Environment, c.environment) {
				t.Errorf("Environment = %+v, want %+v", r.Environment, c.environment)
			}

			for id, want := range c.wantStatus {
				got := statusByID(r, id)
				if got != want {
					t.Errorf("%s status = %q, want %q", id, got, want)
				}
			}
			for id, want := range c.wantContains {
				got := messageByID(r, id)
				if !strings.Contains(got, want) {
					t.Errorf("%s message = %q, want it to contain %q", id, got, want)
				}
			}

			if c.name == "metadata_from_params" {
				if r.SchemaVersion != 1 {
					t.Errorf("SchemaVersion = %d, want 1", r.SchemaVersion)
				}
				if r.CBVersion != version {
					t.Errorf("CBVersion = %q, want %q", r.CBVersion, version)
				}
				wantAt := fixedTime.UTC().Format(time.RFC3339)
				if r.GeneratedAt != wantAt {
					t.Errorf("GeneratedAt = %q, want %q", r.GeneratedAt, wantAt)
				}
			}

			if c.environment != nil {
				rNil := buildSelfTestReport(version, fixedTime, c.docker, c.outcomes, nil)
				if rNil.OK != r.OK || rNil.Passed != r.Passed || rNil.Failed != r.Failed || rNil.Skipped != r.Skipped {
					t.Errorf("environment changed tally: got OK=%v Passed=%d Failed=%d Skipped=%d, nil env gave OK=%v Passed=%d Failed=%d Skipped=%d", r.OK, r.Passed, r.Failed, r.Skipped, rNil.OK, rNil.Passed, rNil.Failed, rNil.Skipped)
				}
			}
		})
	}
}

func statusByID(r selfTestReport, id string) string {
	for _, c := range r.Checks {
		if c.ID == id {
			return c.Status
		}
	}
	return ""
}

func messageByID(r selfTestReport, id string) string {
	for _, c := range r.Checks {
		if c.ID == id {
			return c.Message
		}
	}
	return ""
}

func ptr(s string) *string { return &s }

func TestBuildSelfTestReportJSONRoundTrip(t *testing.T) {
	ts := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	docker := selfTestCheck{ID: "docker", Status: "pass", Message: "docker 27.0.0"}
	outcomes := map[string]toolSelfTestOutcome{
		"python":    {ImageLocalErr: ptr("missing")},
		"node":      {},
		"jq":        {},
		"terraform": {},
	}
	report := buildSelfTestReport("dev", ts, docker, outcomes, nil)

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var got selfTestReport
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(report, got) {
		t.Fatalf("round-trip mismatch:\noriginal: %+v\nunmarshaled: %+v", report, got)
	}

	// Also verify the documented JSON field names appear in the raw output.
	keys := []string{
		"schema_version", "cb_version", "generated_at",
		"checks", "passed", "failed", "skipped", "ok",
		"id", "status", "message",
	}
	raw := string(data)
	for _, k := range keys {
		if !strings.Contains(raw, "\""+k+"\"") {
			t.Errorf("JSON output missing key %q", k)
		}
	}
}

// TestSelfTestStepsDependencyOrder pins the ordering invariant documented on
// selfTestSteps: every step's depID must be "docker" or the id of an earlier
// step in the slice. buildSelfTestReport looks dependencies up in a map that
// is populated in this slice's order, so a step listed before its own
// dependency would silently see a zero-value selfTestCheck (empty ID/Status)
// instead of the real dependency's status — this test makes that reordering
// a build failure instead of a silent, hard-to-notice bug (raised by Devin
// Review on PR #21).
func TestSelfTestStepsDependencyOrder(t *testing.T) {
	seen := map[string]bool{"docker": true}
	for i, step := range selfTestSteps {
		if !seen[step.depID] {
			t.Fatalf("selfTestSteps[%d] (%s) depends on %q, which has not appeared earlier in the slice", i, step.id, step.depID)
		}
		seen[step.id] = true
	}
}

func TestParseSelfTestArgs(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantJSON    bool
		wantRelease bool
		wantErr     bool
	}{
		{"no_args", nil, false, false, false},
		{"json_only", []string{"--json"}, true, false, false},
		{"release_only", []string{"--release"}, false, true, false},
		{"json_then_release", []string{"--json", "--release"}, true, true, false},
		{"release_then_json", []string{"--release", "--json"}, true, true, false},
		{"duplicate_json", []string{"--json", "--json"}, true, false, false},
		{"unknown_token", []string{"--json", "--verbose"}, false, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotJSON, gotRelease, err := parseSelfTestArgs(c.args)
			if (err != nil) != c.wantErr {
				t.Fatalf("parseSelfTestArgs(%v) err = %v, wantErr %v", c.args, err, c.wantErr)
			}
			if !c.wantErr {
				if gotJSON != c.wantJSON {
					t.Errorf("jsonOut = %v, want %v", gotJSON, c.wantJSON)
				}
				if gotRelease != c.wantRelease {
					t.Errorf("release = %v, want %v", gotRelease, c.wantRelease)
				}
			}
		})
	}
}

func TestParseWindowsHostVersionInfo(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		wantWindows string
		wantPS      string
	}{
		{"both_in_order", "windows=Microsoft Windows NT 10.0.26200.0\npowershell=5.1.22621.3672", "Microsoft Windows NT 10.0.26200.0", "5.1.22621.3672"},
		{"both_out_of_order", "powershell=5.1.22621.3672\nwindows=Microsoft Windows NT 10.0.26200.0", "Microsoft Windows NT 10.0.26200.0", "5.1.22621.3672"},
		{"windows_only", "windows=Microsoft Windows NT 10.0.26200.0", "Microsoft Windows NT 10.0.26200.0", ""},
		{"powershell_only", "powershell=5.1.22621.3672", "", "5.1.22621.3672"},
		{"empty", "", "", ""},
		{"unexpected_label", "windows=10.0.26200.0\nsomething=else\npowershell=5.1.22621.3672", "10.0.26200.0", "5.1.22621.3672"},
		{"empty_value", "windows=\npowershell=5.1.22621.3672", "", "5.1.22621.3672"},
		{"malformed_no_equals", "windows\npowershell=5.1.22621.3672", "", "5.1.22621.3672"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotWindows, gotPS := parseWindowsHostVersionInfo(c.raw)
			if gotWindows != c.wantWindows {
				t.Errorf("windowsVersion = %q, want %q", gotWindows, c.wantWindows)
			}
			if gotPS != c.wantPS {
				t.Errorf("powershellVersion = %q, want %q", gotPS, c.wantPS)
			}
		})
	}
}

func TestDockerEngineVersionCheck(t *testing.T) {
	cases := []struct {
		name         string
		docker       selfTestCheck
		wantStatus   string
		wantContains string
	}{
		{"docker_pass", selfTestCheck{ID: "docker", Status: "pass", Message: "docker 27.0.0"}, "pass", "docker 27.0.0"},
		{"docker_fail", selfTestCheck{ID: "docker", Status: "fail", Message: "docker unavailable"}, "skip", "docker unavailable"},
		{"docker_skip", selfTestCheck{ID: "docker", Status: "skip", Message: "skipped: docker unavailable"}, "skip", "docker unavailable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := dockerEngineVersionCheck(c.docker)
			if got.ID != "docker-engine-version" {
				t.Errorf("ID = %q, want %q", got.ID, "docker-engine-version")
			}
			if got.Status != c.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, c.wantStatus)
			}
			if !strings.Contains(got.Message, c.wantContains) {
				t.Errorf("Message = %q, want it to contain %q", got.Message, c.wantContains)
			}
		})
	}
}

func TestEnvCheckFromVerdict(t *testing.T) {
	cases := []struct {
		status  string
		message string
		want    selfTestCheck
	}{
		{"ok", "all good", selfTestCheck{ID: "test-id", Status: "pass", Message: "all good"}},
		{"warn", "be careful", selfTestCheck{ID: "test-id", Status: "skip", Message: "be careful"}},
		{"fail", "bad", selfTestCheck{ID: "test-id", Status: "fail", Message: "bad"}},
	}
	for _, c := range cases {
		t.Run(c.status, func(t *testing.T) {
			got := envCheckFromVerdict("test-id", c.status, c.message)
			if got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestSelfTestReportEnvironmentOmitEmpty(t *testing.T) {
	ts := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	docker := selfTestCheck{ID: "docker", Status: "pass", Message: "docker 27.0.0"}
	outcomes := map[string]toolSelfTestOutcome{}

	nilEnv := buildSelfTestReport("dev", ts, docker, outcomes, nil)
	data, err := json.Marshal(nilEnv)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(data), "\"environment\"") {
		t.Errorf("nil environment should be omitted from JSON; got %s", data)
	}

	env := []selfTestCheck{
		{ID: "windows-version", Status: "pass", Message: "Microsoft Windows NT 10.0.26200.0"},
		{ID: "powershell-version", Status: "pass", Message: "5.1.22621.3672"},
		{ID: "docker-engine-version", Status: "pass", Message: "docker 27.0.0"},
		{ID: "docker-os-type", Status: "pass", Message: "docker is in Linux-container mode"},
		{ID: "cwd-reparse-point", Status: "pass", Message: "current directory does not sit behind a reparse point"},
		{ID: "shim-dir-network-storage", Status: "pass", Message: "shim directory is on a Fixed drive (C:\\Tools)"},
		{ID: "cwd-network-storage", Status: "pass", Message: "current directory is on a Fixed drive (C:\\Work)"},
	}
	populated := buildSelfTestReport("dev", ts, docker, outcomes, env)
	data, err = json.Marshal(populated)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(data), "\"environment\"") {
		t.Errorf("non-nil environment should appear in JSON; got %s", data)
	}
	for _, id := range []string{"windows-version", "powershell-version", "docker-engine-version", "docker-os-type", "cwd-reparse-point", "shim-dir-network-storage", "cwd-network-storage"} {
		if !strings.Contains(string(data), "\""+id+"\"") {
			t.Errorf("JSON output missing environment id %q", id)
		}
	}
}
