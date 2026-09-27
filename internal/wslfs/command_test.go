package wslfs

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

func TestCommandCheckIsReadOnlyAndReportsPlan(t *testing.T) {
	layout := commandTestLayout()
	wantPlan := Plan{Layout: layout, MissingDirectories: []string{"/home/alice/.config/container-bin"}}
	prepared := false
	c := command{
		currentLayout: func() (hostenv.WSLLayout, error) { return layout, nil },
		check: func(got hostenv.WSLLayout) (Plan, error) {
			if got != layout {
				t.Fatalf("check layout = %+v", got)
			}
			return wantPlan, nil
		},
		prepare: func(hostenv.WSLLayout) error { prepared = true; return nil },
	}
	var out bytes.Buffer
	if err := c.run([]string{"prepare", "--check"}, &out); err != nil {
		t.Fatal(err)
	}
	if prepared {
		t.Fatal("check mode prepared the filesystem")
	}
	for _, want := range []string{"read-only; no files changed", "LAYOUT PREPARATION REQUIRED", wantPlan.MissingDirectories[0], "cb wsl prepare --apply", layout.StateNamespace} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("check output missing %q:\n%s", want, out.String())
		}
	}
}

func TestCommandApplyPreparesThenRechecks(t *testing.T) {
	layout := commandTestLayout()
	var sequence []string
	c := command{
		currentLayout: func() (hostenv.WSLLayout, error) {
			sequence = append(sequence, "layout")
			return layout, nil
		},
		prepare: func(got hostenv.WSLLayout) error {
			sequence = append(sequence, "prepare")
			if got != layout {
				t.Fatalf("prepare layout = %+v", got)
			}
			return nil
		},
		check: func(got hostenv.WSLLayout) (Plan, error) {
			sequence = append(sequence, "check")
			return Plan{Layout: got}, nil
		},
	}
	var out bytes.Buffer
	if err := c.run([]string{"prepare", "--apply"}, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sequence, []string{"layout", "prepare", "check"}) {
		t.Fatalf("apply sequence = %v", sequence)
	}
	if !strings.Contains(out.String(), "prepared and revalidated") || !strings.Contains(out.String(), "status:        LAYOUT READY") {
		t.Fatalf("apply output = %q", out.String())
	}
}

func TestCommandFailsClosed(t *testing.T) {
	layout := commandTestLayout()
	for _, args := range [][]string{nil, {"prepare"}, {"prepare", "--yes"}, {"setup", "--apply"}, {"prepare", "--check", "extra"}} {
		if err := (command{}).run(args, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "usage") {
			t.Errorf("run(%q) error = %v", args, err)
		}
	}
	if err := (command{}).run([]string{"prepare", "--check"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete command error = %v", err)
	}

	layoutErr := errors.New("layout unavailable")
	if err := (command{
		currentLayout: func() (hostenv.WSLLayout, error) { return hostenv.WSLLayout{}, layoutErr },
		check:         func(hostenv.WSLLayout) (Plan, error) { panic("check called after layout failure") },
		prepare:       func(hostenv.WSLLayout) error { panic("prepare called after layout failure") },
	}).run([]string{"prepare", "--check"}, &bytes.Buffer{}); !errors.Is(err, layoutErr) {
		t.Fatalf("layout error = %v", err)
	}

	prepareErr := errors.New("preparation failed")
	if err := (command{
		currentLayout: func() (hostenv.WSLLayout, error) { return layout, nil },
		check:         func(hostenv.WSLLayout) (Plan, error) { panic("check called after preparation failure") },
		prepare:       func(hostenv.WSLLayout) error { return prepareErr },
	}).run([]string{"prepare", "--apply"}, &bytes.Buffer{}); !errors.Is(err, prepareErr) {
		t.Fatalf("prepare error = %v", err)
	}

	want := errors.New("unsafe layout")
	if err := (command{
		currentLayout: func() (hostenv.WSLLayout, error) { return layout, nil },
		check:         func(hostenv.WSLLayout) (Plan, error) { return Plan{}, want },
		prepare:       func(hostenv.WSLLayout) error { return nil },
	}).run([]string{"prepare", "--check"}, &bytes.Buffer{}); !errors.Is(err, want) {
		t.Fatalf("check error = %v", err)
	}

	if err := (command{
		currentLayout: func() (hostenv.WSLLayout, error) { return layout, nil },
		check: func(hostenv.WSLLayout) (Plan, error) {
			return Plan{Layout: layout, MissingDirectories: []string{"missing"}}, nil
		},
		prepare: func(hostenv.WSLLayout) error { return nil },
	}).run([]string{"prepare", "--apply"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "without creating every required directory") {
		t.Fatalf("incomplete apply error = %v", err)
	}

	if err := (command{
		currentLayout: func() (hostenv.WSLLayout, error) { return layout, nil },
		check: func(hostenv.WSLLayout) (Plan, error) {
			plan := Plan{Layout: layout}
			plan.Layout.StateNamespace = "wsl2-other"
			return plan, nil
		},
		prepare: func(hostenv.WSLLayout) error { return nil },
	}).run([]string{"prepare", "--check"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "inconsistent identity") {
		t.Fatalf("inconsistent layout error = %v", err)
	}
}

func TestCommandPropagatesReportWriteFailure(t *testing.T) {
	layout := commandTestLayout()
	for _, mode := range []string{"--check", "--apply"} {
		t.Run(mode, func(t *testing.T) {
			err := (command{
				currentLayout: func() (hostenv.WSLLayout, error) { return layout, nil },
				check:         func(hostenv.WSLLayout) (Plan, error) { return Plan{Layout: layout}, nil },
				prepare:       func(hostenv.WSLLayout) error { return nil },
			}).run([]string{"prepare", mode}, failingWriter{})
			if !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("run(%s) error = %v, want closed pipe", mode, err)
			}
		})
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

func commandTestLayout() hostenv.WSLLayout {
	return hostenv.WSLLayout{
		Distro: "Ubuntu-24.04", UID: 1000, Home: "/home/alice",
		BinaryPath: "/home/alice/.local/lib/container-bin/cb", ManagementShim: "/home/alice/.local/bin/cb",
		ShimDir: "/home/alice/.local/bin", ConfigDir: "/home/alice/.config/container-bin",
		RegistryPath: "/home/alice/.config/container-bin/container-bin.toml", LockPath: "/home/alice/.config/container-bin/container-bin.lock",
		StateDir: "/home/alice/.local/state/container-bin", StateNamespace: "wsl2-0123456789abcdef0123456789abcdef",
	}
}
