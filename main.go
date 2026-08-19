package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/AviBackToBlack/container-bin/internal/cli"
	"github.com/AviBackToBlack/container-bin/internal/diag"
	"github.com/AviBackToBlack/container-bin/internal/dockerrun"
	"github.com/AviBackToBlack/container-bin/internal/mutationlock"
	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/state"
)

// version is injected at release time via:
//
//	go build -ldflags "-X main.version=v1.2.3"
//
// Local/dev builds report "dev".
var version = "dev"

func main() {
	reg, cfgPath, err := registry.Load()
	if err != nil {
		fatalf("registry: %v", err)
	}

	invoked := invokedName(os.Args[0])
	if invoked != "cb" && invoked != "container-bin" && !strings.HasPrefix(invoked, "cb-v") {
		tool, ok := reg.Tools[invoked]
		if !ok {
			fatalf("no tool profile for %q (registry: %s)", invoked, cfgPath)
		}
		code, err := dockerrun.RunTool(tool, os.Args[1:])
		if err != nil {
			fatalf("%v", err)
		}
		os.Exit(code)
	}

	if len(os.Args) < 2 {
		usage(cfgPath)
		return
	}
	switch os.Args[1] {
	case "install":
		if err := withMutationLock(cfgPath, func() error {
			return cli.Install(cfgPath, version)
		}); err != nil {
			fatalf("install: %v", err)
		}
	case "setup":
		if err := withMutationLock(cfgPath, func() error {
			return cli.Setup(cfgPath, version)
		}); err != nil {
			fatalf("setup: %v", err)
		}
	case "doctor":
		if err := diag.Doctor(reg, cfgPath); err != nil {
			fatalf("doctor: %v", err)
		}
	case "bugreport":
		if err := diag.Bugreport(reg, cfgPath, version); err != nil {
			fatalf("bugreport: %v", err)
		}
	case "backup":
		if err := cli.Backup(cfgPath, os.Args[2:], version); err != nil {
			fatalf("backup: %v", err)
		}
	case "restore":
		if err := withMutationLock(cfgPath, func() error {
			return cli.Restore(cfgPath, os.Args[2:])
		}); err != nil {
			fatalf("restore: %v", err)
		}
	case "self-test":
		jsonOut, release, err := diag.ParseSelfTestArgs(os.Args[2:])
		if err != nil {
			fatalf("self-test: %v", err)
		}
		if err := diag.SelfTest(reg, jsonOut, release, version); err != nil {
			fatalf("self-test: %v", err)
		}
	case "list":
		registry.ListTools(reg, cfgPath)
	case "trace":
		if err := cli.Trace(reg, os.Args[2:]); err != nil {
			fatalf("trace: %v", err)
		}
	case "env":
		if err := cli.Env(reg); err != nil {
			fatalf("env: %v", err)
		}
	case "state":
		if err := state.Show(reg); err != nil {
			fatalf("state: %v", err)
		}
	case "inspect":
		if err := cli.Inspect(reg, os.Args[2:]); err != nil {
			fatalf("inspect: %v", err)
		}
	case "gc":
		if err := state.GC(reg, os.Args[2:]); err != nil {
			fatalf("gc: %v", err)
		}
	case "expose":
		if err := withMutationLock(cfgPath, func() error {
			reg, _, err := registry.Load()
			if err != nil {
				return err
			}
			return cli.Expose(reg, cfgPath, os.Args[2:])
		}); err != nil {
			fatalf("expose: %v", err)
		}
	case "unexpose":
		if err := withMutationLock(cfgPath, func() error {
			reg, _, err := registry.Load()
			if err != nil {
				return err
			}
			return cli.Unexpose(reg, cfgPath, os.Args[2:])
		}); err != nil {
			fatalf("unexpose: %v", err)
		}
	case "uninstall":
		if err := withMutationLock(cfgPath, func() error {
			reg, _, err := registry.Load()
			if err != nil {
				return err
			}
			return cli.Uninstall(reg, cfgPath, os.Args[2:])
		}); err != nil {
			fatalf("uninstall: %v", err)
		}
	case "lock":
		if err := withMutationLock(cfgPath, func() error {
			reg, _, err := registry.Load()
			if err != nil {
				return err
			}
			return cli.Lock(reg, cfgPath, os.Args[2:])
		}); err != nil {
			fatalf("lock: %v", err)
		}
	case "update":
		if err := withMutationLock(cfgPath, func() error {
			reg, _, err := registry.Load()
			if err != nil {
				return err
			}
			return cli.Update(reg, cfgPath, os.Args[2:])
		}); err != nil {
			fatalf("update: %v", err)
		}
	case "config":
		fmt.Println(cfgPath)
	case "version", "--version", "-V":
		fmt.Printf("container-bin %s\n", version)
	default:
		usage(cfgPath)
		osExit(exitUsage)
	}
}

// invokedName derives the dispatch name from argv[0]. Windows filenames are
// case-insensitive, so both base and extension are lowered before trimming —
// cmd.exe can hand us PYTHON.EXE, which must still dispatch as "python".
func invokedName(argv0 string) string {
	base := strings.ToLower(filepath.Base(argv0))
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func usage(cfg string) {
	fmt.Printf(`container-bin (cb) %s — Docker-backed Windows CLI shims

Commands:
  cb setup     initialize/upgrade registry, install shims, then run doctor
  cb install   create/update shims from the tool registry
  cb doctor    validate Docker, PATH, shims, registry, lock and managed volumes
  cb bugreport assemble a paste-ready diagnostic report with best-effort redaction
  cb backup    back up registry + lock to a zip
  cb restore   validate/restore a backup (dry-run unless --apply)
  cb self-test [--json] [--release] run offline end-to-end compatibility checks
  cb list      list configured tool profiles
  cb trace     show raw/normalized/mapped argv for a tool without running it
  cb env       show project root and Python environment selected for cwd
  cb state     list container-bin Docker volumes and mark current/shared state
  cb inspect   show a tool profile plus resolved project/state information
  cb gc        dry-run cleanup; supports --orphans for labeled missing projects
  cb expose    expose binaries from a managed global tool store (npm)
  cb unexpose  remove dynamically exposed tool profiles/shims
  cb uninstall remove custom tool profiles/shims
  cb lock      create/check immutable image digest lockfile
  cb update    explicitly refresh one or all locked images
  cb config    print registry path
  cb version   print container-bin version

Registry:
  %s
`, version, cfg)
}

const (
	exitUsage       = 2
	exitCbFailure   = 120
	exitInterrupted = 130
)

// osExit is an indirection so unit tests can observe the exit code. A test
// stub that returns instead of terminating will cause fatalf to continue to
// its caller, so stubs must either panic or otherwise halt the goroutine.
var osExit = os.Exit

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "container-bin: "+format+"\n", args...)
	osExit(exitCbFailure)
}

// withMutationLock is not re-entrant; call sites must not nest another
// withMutationLock-wrapped operation while holding the lock.
func withMutationLock(cfgPath string, fn func() error) error {
	release, err := mutationlock.Acquire(cfgPath, mutationlock.Wait)
	if err != nil {
		return err
	}
	defer release()

	c := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(c, os.Interrupt)
	go func() {
		select {
		case <-c:
			release()
			osExit(exitInterrupted)
		case <-done:
		}
	}()
	defer func() {
		signal.Stop(c)
		close(done)
	}()

	return fn()
}
