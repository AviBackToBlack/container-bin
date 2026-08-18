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
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := buildSelfTestReport(version, fixedTime, c.docker, c.outcomes)

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
	report := buildSelfTestReport("dev", ts, docker, outcomes)

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
