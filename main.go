package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/AviBackToBlack/container-bin/internal/cli"
	"github.com/AviBackToBlack/container-bin/internal/diag"
	"github.com/AviBackToBlack/container-bin/internal/dockerrun"
	"github.com/AviBackToBlack/container-bin/internal/hostenv"
	"github.com/AviBackToBlack/container-bin/internal/mutationlock"
	"github.com/AviBackToBlack/container-bin/internal/policy"
	"github.com/AviBackToBlack/container-bin/internal/projectconfig"
	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/selfupdate"
	"github.com/AviBackToBlack/container-bin/internal/state"
)

// version is injected at release time via:
//
//	go build -ldflags "-X main.version=v1.2.3"
//
// Local/dev builds report "dev".
var version = "dev"

// These test seams prove bootstrap commands return before policy or registry
// I/O. Production always uses the corresponding package loaders.
var loadRegistry = registry.Load
var loadRegistryReadOnly = registry.LoadReadOnly
var loadPolicy = policy.Load

// requireHostFrontend is a test seam around the fail-closed host boundary.
// Production always uses hostenv.RequireFrontend.
var requireHostFrontend = hostenv.RequireFrontend

// runSelfUpdateCheck is a test seam for proving self-update selection remains
// available before policy or registry I/O. Production always uses selfupdate.Check.
var runSelfUpdateCheck = selfupdate.Check

func main() {
	invoked := invokedName(os.Args[0])
	if isManagementInvocation(invoked) && handleBootstrapCommand(os.Args[1:]) {
		return
	}
	if err := requireHostFrontend(); err != nil {
		fatalf("host runtime: %v", err)
		return
	}
	if isManagementInvocation(invoked) && len(os.Args) > 1 && os.Args[1] == "self-update" {
		if err := runSelfUpdateCheck(context.Background(), version, os.Args[2:], os.Stdout); err != nil {
			fatalf("self-update: %v", err)
		}
		return
	}
	machinePolicy, err := loadPolicy()
	if err != nil {
		fatalf("machine policy: %v", err)
	}

	registryLoader := loadRegistry
	if useReadOnlyRegistryLoad(invoked, os.Args[1:]) {
		registryLoader = loadRegistryReadOnly
	}
	reg, cfgPath, err := registryLoader(machinePolicy.AuthenticateRegistry)
	if err != nil {
		fatalf("registry: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		fatalf("project overlay: determine current working directory: %v", err)
	}
	projectContext, _ := projectconfig.InspectDefault(reg, cwd)

	if isManagementInvocation(invoked) {
		switch os.Args[1] {
		case "trust":
			check := hasArg(os.Args[2:], "--check")
			run := func() error {
				loader := registry.Load
				if check {
					loader = registry.LoadReadOnly
				}
				fresh, _, err := loader(machinePolicy.AuthenticateRegistry)
				if err != nil {
					return err
				}
				ctx, freshTrustPath := projectconfig.InspectDefault(fresh, cwd)
				return projectconfig.Trust(ctx, freshTrustPath, os.Args[2:], os.Stdin, os.Stdout, stdinInteractive(), machinePolicy, registry.InstallAdditionalShimNames)
			}
			if check {
				err = run()
			} else {
				err = withMutationLock(cfgPath, run)
			}
			if err != nil {
				fatalf("trust: %v", err)
			}
			return
		case "untrust":
			if len(os.Args) != 2 {
				fatalf("untrust: usage: cb untrust")
			}
			if err := withMutationLock(cfgPath, func() error {
				return projectconfig.UntrustDefault(cwd, os.Stdout)
			}); err != nil {
				fatalf("untrust: %v", err)
			}
			return
		case "inspect":
			if len(os.Args) == 3 && os.Args[2] == "--project" {
				if err := projectContext.PrintReview(os.Stdout, machinePolicy); err != nil {
					fatalf("inspect: %v", err)
				}
				return
			}
		case "doctor":
			doctorReg := reg
			projectErr := projectDoctorStatus(projectContext)
			if projectContext.Status == projectconfig.Trusted {
				doctorReg = projectContext.Effective
			}
			doctorErr := diag.Doctor(doctorReg, cfgPath, machinePolicy)
			if projectErr != nil {
				fatalf("doctor: %v", projectErr)
			}
			if doctorErr != nil {
				fatalf("doctor: %v", doctorErr)
			}
			return
		}
	}

	reg, err = projectContext.Registry(reg)
	if err != nil {
		fatalf("project overlay: %v", err)
	}
	loadFreshEffective := func() (registry.Registry, error) {
		fresh, _, err := registry.Load(machinePolicy.AuthenticateRegistry)
		if err != nil {
			return registry.Registry{}, err
		}
		ctx, _ := projectconfig.InspectDefault(fresh, cwd)
		return ctx.Registry(fresh)
	}

	if !isManagementInvocation(invoked) {
		tool, _, ok := reg.Resolve(invoked)
		if !ok {
			fatalf("no tool profile for %q (registry: %s)", invoked, cfgPath)
		}
		code, err := dockerrun.RunTool(tool, os.Args[1:], machinePolicy)
		if err != nil {
			fatalf("%v", err)
		}
		os.Exit(code)
	}

	switch os.Args[1] {
	case "install":
		if err := withMutationLock(cfgPath, func() error {
			return cli.Install(cfgPath, version, machinePolicy)
		}); err != nil {
			fatalf("install: %v", err)
		}
	case "add":
		if err := withMutationLock(cfgPath, func() error {
			reg, err := loadFreshEffective()
			if err != nil {
				return err
			}
			return cli.Add(reg, cfgPath, os.Args[2:], machinePolicy)
		}); err != nil {
			fatalf("add: %v", err)
		}
	case "setup":
		if err := withMutationLock(cfgPath, func() error {
			return cli.Setup(cfgPath, version, machinePolicy)
		}); err != nil {
			fatalf("setup: %v", err)
		}
	case "bugreport":
		if err := diag.Bugreport(reg, cfgPath, version, machinePolicy); err != nil {
			fatalf("bugreport: %v", err)
		}
	case "backup":
		if err := withMutationLock(cfgPath, func() error {
			return cli.Backup(cfgPath, os.Args[2:], version, machinePolicy)
		}); err != nil {
			fatalf("backup: %v", err)
		}
	case "restore":
		if err := withMutationLock(cfgPath, func() error {
			return cli.Restore(cfgPath, os.Args[2:], machinePolicy)
		}); err != nil {
			fatalf("restore: %v", err)
		}
	case "self-test":
		jsonOut, release, err := diag.ParseSelfTestArgs(os.Args[2:])
		if err != nil {
			fatalf("self-test: %v", err)
		}
		if err := diag.SelfTest(reg, jsonOut, release, version, machinePolicy); err != nil {
			fatalf("self-test: %v", err)
		}
	case "list":
		registry.ListTools(reg, cfgPath)
	case "default":
		if len(os.Args) > 2 && os.Args[2] == "set" {
			if err := withMutationLock(cfgPath, func() error {
				fresh, _, err := registry.Load(machinePolicy.AuthenticateRegistry)
				if err != nil {
					return err
				}
				validateProjectOverlay := func(candidate registry.Registry) error {
					ctx, _ := projectconfig.InspectDefault(candidate, cwd)
					if _, err := ctx.Registry(candidate); err != nil {
						return fmt.Errorf("default selection conflicts with current project overlay: %w", err)
					}
					return nil
				}
				return cli.Default(fresh, cfgPath, os.Args[2:], machinePolicy, validateProjectOverlay)
			}); err != nil {
				fatalf("default: %v", err)
			}
		} else if err := cli.Default(reg, cfgPath, os.Args[2:], machinePolicy); err != nil {
			fatalf("default: %v", err)
		}
	case "trace":
		if err := cli.Trace(reg, os.Args[2:], machinePolicy); err != nil {
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
		if err := cli.Inspect(reg, os.Args[2:], machinePolicy); err != nil {
			fatalf("inspect: %v", err)
		}
	case "gc":
		if err := state.GC(reg, os.Args[2:]); err != nil {
			fatalf("gc: %v", err)
		}
	case "expose":
		if err := withMutationLock(cfgPath, func() error {
			fresh, _, err := registry.Load(machinePolicy.AuthenticateRegistry)
			if err != nil {
				return err
			}
			ctx, _ := projectconfig.InspectDefault(fresh, cwd)
			effective, err := ctx.Registry(fresh)
			if err != nil {
				return err
			}
			args := os.Args[2:]
			if err := rejectProjectExposeSource(ctx, args); err != nil {
				return err
			}
			return cli.Expose(effective, cfgPath, args, machinePolicy)
		}); err != nil {
			fatalf("expose: %v", err)
		}
	case "unexpose":
		if err := withMutationLock(cfgPath, func() error {
			reg, _, err := registry.Load(machinePolicy.AuthenticateRegistry)
			if err != nil {
				return err
			}
			return cli.Unexpose(reg, cfgPath, os.Args[2:], machinePolicy)
		}); err != nil {
			fatalf("unexpose: %v", err)
		}
	case "uninstall":
		if err := withMutationLock(cfgPath, func() error {
			reg, _, err := registry.Load(machinePolicy.AuthenticateRegistry)
			if err != nil {
				return err
			}
			return cli.Uninstall(reg, cfgPath, os.Args[2:], machinePolicy)
		}); err != nil {
			fatalf("uninstall: %v", err)
		}
	case "lock":
		if err := withMutationLock(cfgPath, func() error {
			fresh, _, err := registry.Load(machinePolicy.AuthenticateRegistry)
			if err != nil {
				return err
			}
			ctx, _ := projectconfig.InspectDefault(fresh, cwd)
			effective, err := ctx.Registry(fresh)
			if err != nil {
				return err
			}
			if ctx.Status == projectconfig.Trusted {
				return cli.LockPreservingUnconfigured(effective, cfgPath, os.Args[2:], machinePolicy)
			}
			return cli.Lock(effective, cfgPath, os.Args[2:], machinePolicy)
		}); err != nil {
			fatalf("lock: %v", err)
		}
	case "update":
		if err := withMutationLock(cfgPath, func() error {
			reg, err := loadFreshEffective()
			if err != nil {
				return err
			}
			return cli.Update(reg, cfgPath, os.Args[2:], machinePolicy)
		}); err != nil {
			fatalf("update: %v", err)
		}
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

func isManagementInvocation(invoked string) bool {
	return invoked == "cb" || invoked == "container-bin" || registry.IsVersionedBinaryName(invoked)
}

func useReadOnlyRegistryLoad(invoked string, args []string) bool {
	if !isManagementInvocation(invoked) || len(args) == 0 {
		return false
	}
	switch args[0] {
	case "trust":
		// The mutating trust path reloads with recovery only after acquiring the
		// mutation lock; its initial review must remain read-only too.
		return true
	case "inspect":
		return len(args) == 2 && args[1] == "--project"
	default:
		return false
	}
}

// handleBootstrapCommand serves commands that must remain available when the
// registry is missing, corrupt, or was written by a newer container-bin. It
// deliberately resolves only the registry path for help/config output; it
// never reads or validates container-bin.toml.
func handleBootstrapCommand(args []string) bool {
	if len(args) == 0 {
		usage(bootstrapRegistryPath())
		return true
	}
	switch args[0] {
	case "version", "--version", "-V":
		fmt.Printf("container-bin %s\n", version)
	case "help", "--help", "-h":
		usage(bootstrapRegistryPath())
	case "config":
		fmt.Println(bootstrapRegistryPath())
	default:
		return false
	}
	return true
}

func bootstrapRegistryPath() string {
	cfgPath, err := registry.Path()
	if err != nil {
		fatalf("registry path: %v", err)
	}
	return cfgPath
}

func usage(cfg string) {
	fmt.Printf(`container-bin (cb) %s — Docker-backed Windows CLI shims

Commands:
  cb setup     initialize/upgrade registry, install shims, then run doctor
  cb install   create/update shims from the tool registry
  cb add       add a minimal stateless tool profile; --local declares local intent
  cb doctor    validate Docker, PATH, shims, registry, lock and managed volumes
  cb bugreport assemble a paste-ready diagnostic report with best-effort redaction
  cb backup    back up registry + lock; --state adds explicitly named volumes
  cb restore   validate/restore a backup (dry-run unless --apply; state is opt-in)
  cb self-test [--json] [--release] run offline end-to-end compatibility checks
  cb self-update --check [--prerelease | --version VERSION] [--allow-downgrade]
                report a release update plan without downloading or changing files
  cb list      list configured tool profiles
  cb default   list defaults; "cb default set FAMILY VERSION" switches a family
  cb trace     show raw/normalized/mapped argv for a tool without running it
  cb env       show project root and Python environment selected for cwd
  cb state     list container-bin Docker volumes and mark current/shared state
  cb inspect   show a tool profile plus resolved project/state information
  cb inspect --project  review the current project overlay and trust digest
  cb trust [--check | --yes]  explicitly trust the current .container-bin.toml
  cb untrust   revoke trust for the current project overlay
  cb gc        dry-run cleanup; supports --orphans for labeled missing projects
  cb expose    expose managed-store binaries or one explicit shared-volume file
  cb unexpose  remove dynamically exposed tool profiles/shims
  cb uninstall remove custom tool profiles/shims
  cb lock      create/check immutable image digest lockfile
  cb update    explicitly refresh one or all locked images
  cb config    print registry path
  cb version   print container-bin version
  cb help      print this help without loading the registry

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

func stdinInteractive() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func rejectProjectExposeSource(ctx projectconfig.Context, args []string) error {
	sourceIndex := 0
	if len(args) > 0 && args[0] == "--shared-file" {
		sourceIndex = 1
	}
	if sourceIndex >= len(args) {
		return nil
	}
	source := strings.ToLower(args[sourceIndex])
	if _, projectLocal := ctx.Overlay.Registry.Tools[source]; projectLocal {
		return fmt.Errorf("tool %q is project-local; refusing to persist derived profiles in the global registry", source)
	}
	return nil
}

func projectDoctorStatus(ctx projectconfig.Context) error {
	switch ctx.Status {
	case projectconfig.Absent:
		fmt.Println("OK       project overlay: absent")
		return nil
	case projectconfig.Trusted:
		fmt.Printf("OK       project overlay: %s\n", ctx.Summary())
		return nil
	case projectconfig.Untrusted:
		fmt.Printf("FAIL     project overlay: %s\n", ctx.Summary())
		return errors.New("project overlay is not trusted; run `cb trust --check`")
	case projectconfig.Changed:
		fmt.Printf("FAIL     project overlay: %s\n", ctx.Summary())
		return errors.New("project overlay changed after trust; review and trust the new digest")
	default:
		fmt.Printf("FAIL     project overlay: %s\n", ctx.Summary())
		if ctx.Err != nil {
			return errors.New("project overlay is invalid; see the escaped diagnostic above")
		}
		return fmt.Errorf("project overlay has unknown status %q", ctx.Status)
	}
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
