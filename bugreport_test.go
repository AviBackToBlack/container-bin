package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestCaptureStdoutCapturesAndRestores(t *testing.T) {
	old := os.Stdout
	got, err := captureStdout(func() error {
		fmt.Println("hello, bugreport capture")
		return nil
	})
	if err != nil {
		t.Fatalf("captureStdout: %v", err)
	}
	if !strings.Contains(got, "hello, bugreport capture") {
		t.Errorf("captured output does not contain expected text: %q", got)
	}
	if os.Stdout != old {
		t.Fatalf("os.Stdout was not restored: got %p, want %p", os.Stdout, old)
	}
}

func TestCaptureStdoutIgnoresFnError(t *testing.T) {
	got, err := captureStdout(func() error {
		fmt.Fprint(os.Stdout, "output despite error")
		return errors.New("intentional fn error")
	})
	if err != nil {
		t.Fatalf("captureStdout returned error: %v", err)
	}
	if got != "output despite error" {
		t.Errorf("captured output = %q, want %q", got, "output despite error")
	}
}

func TestRedactSecrets(t *testing.T) {
	awsKey := "AKIAIOSFODNN7EXAMPLE"
	githubToken := "ghp_1234567890abcdef1234567890abcdef1234"
	bearerToken := "eyJhbGci.example.signature"

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "kv_password",
			in:   "password=secret123",
			want: "password=«redacted»",
		},
		{
			name: "kv_colon_separator",
			in:   "api_key: abcdef",
			want: "api_key=«redacted»",
		},
		{
			name: "kv_case_insensitive",
			in:   "CLIENT_SECRET=supersecret",
			want: "CLIENT_SECRET=«redacted»",
		},
		{
			name: "aws_access_key_id",
			in:   fmt.Sprintf("AWS_SECRET_ACCESS_KEY=%s", awsKey),
			want: "AWS_SECRET_ACCESS_KEY=«redacted»",
		},
		{
			name: "github_token",
			// Deliberately not "token=<value>" here: "token" is itself in the
			// KV allowlist, so a KV-shaped input would be redacted by that
			// pattern before the GitHub-specific one ever runs, and this case
			// would keep passing even if githubTokenPattern were broken or
			// removed. Phrasing it as prose without a "token=" assignment
			// isolates the GitHub pattern instead.
			in:   fmt.Sprintf("found a leaked token in logs: %s", githubToken),
			want: "found a leaked token in logs: «redacted»",
		},
		{
			name: "bearer_token",
			in:   fmt.Sprintf("Authorization: Bearer %s", bearerToken),
			want: "Authorization: Bearer «redacted»",
		},
		{
			name: "no_false_positive_password_substring",
			in:   "setting up a passwordless-auth-flow is recommended",
			want: "setting up a passwordless-auth-flow is recommended",
		},
		{
			name: "doctor_style_line_unchanged",
			in:   "WARN     could not inspect shim directory permissions: exit status 1",
			want: "WARN     could not inspect shim directory permissions: exit status 1",
		},
		{
			name: "listtools_style_line_unchanged",
			in:   "python     python:3.13-slim                python/python",
			want: "python     python:3.13-slim                python/python",
		},
		{
			name: "no_secrets",
			in:   "this report is clean and safe to post",
			want: "this report is clean and safe to post",
		},
		{
			name: "multiple_secrets",
			in:   fmt.Sprintf("password=hunter2 and the aws key is %s", awsKey),
			want: "password=«redacted» and the aws key is «redacted»",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := redactSecrets(c.in)
			if got != c.want {
				t.Errorf("redactSecrets(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
