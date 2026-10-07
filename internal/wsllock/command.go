// Package wsllock owns the explicit native-WSL image lock lifecycle. It uses
// Docker Desktop's proof-bound Engine transport rather than a Docker CLI or
// ambient context, and writes only the fixed private WSL lockfile path.
package wsllock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
	"github.com/AviBackToBlack/container-bin/internal/lockfile"
	"github.com/AviBackToBlack/container-bin/internal/policy"
	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/wsldocker"
	"github.com/AviBackToBlack/container-bin/internal/wslfs"
)

const usage = "usage: cb wsl lock --check | cb wsl lock --apply [--local TOOL ...]"

type lockFunc func(string, func() error) error

type imageIdentity struct {
	id          string
	repoDigests []string
}

type command struct {
	currentLayout         func() (hostenv.WSLLayout, error)
	checkLayout           func(hostenv.WSLLayout) (wslfs.Plan, error)
	checkRegistryRecovery func(hostenv.WSLLayout) error
	checkLockRecovery     func(hostenv.WSLLayout) error
	loadPolicy            func() (policy.Policy, error)
	loadRegistry          func(string, registry.Authenticator) (registry.Registry, string, error)
	loadRegistryReadOnly  func(string, registry.Authenticator) (registry.Registry, string, error)
	loadLock              func(string) (*lockfile.LockFile, error)
	loadLockReadOnly      func(string) (*lockfile.LockFile, error)
	pullImage             func(context.Context, string) error
	inspectImage          func(context.Context, string) (imageIdentity, error)
	writeLock             func(string, *lockfile.LockFile, os.FileMode) error
	withLock              lockFunc
}

// Run checks or refreshes the fixed native-WSL image lock. Apply supports
// anonymous/public registry pulls and explicit local-image intent. Private
// registry credential bridging and native-WSL signature evidence production
// remain fail-closed rather than consulting ambient Docker configuration.
func Run(ctx context.Context, args []string, out io.Writer, withLock lockFunc) error {
	return (command{
		currentLayout:         wslfs.CurrentLayout,
		checkLayout:           wslfs.Check,
		checkRegistryRecovery: wslfs.CheckRegistryRecovery,
		checkLockRecovery:     wslfs.CheckLockRecovery,
		loadPolicy:            policy.Load,
		loadRegistry:          registry.LoadAt,
		loadRegistryReadOnly:  registry.LoadAtReadOnly,
		loadLock:              lockfile.Load,
		loadLockReadOnly:      lockfile.LoadReadOnly,
		pullImage:             wsldocker.PullImage,
		inspectImage: func(ctx context.Context, reference string) (imageIdentity, error) {
			snapshot, err := wsldocker.InspectImage(ctx, reference)
			return imageIdentity{id: snapshot.ID(), repoDigests: snapshot.RepoDigests()}, err
		},
		writeLock: lockfile.WriteMode,
		withLock:  withLock,
	}).run(ctx, args, out)
}

func (c command) run(ctx context.Context, args []string, out io.Writer) error {
	if ctx == nil {
		return errors.New("native WSL image lock requires a context")
	}
	if out == nil {
		return errors.New("native WSL image lock requires an output writer")
	}
	apply, localTools, err := parseArgs(args)
	if err != nil {
		return err
	}
	if c.currentLayout == nil || c.checkLayout == nil || c.checkRegistryRecovery == nil || c.checkLockRecovery == nil || c.loadPolicy == nil || c.loadRegistryReadOnly == nil || c.loadLockReadOnly == nil || c.inspectImage == nil {
		return errors.New("native WSL image lock command is incomplete")
	}
	layout, err := c.currentLayout()
	if err != nil {
		return fmt.Errorf("derive native WSL layout: %w", err)
	}
	if err := c.validateLayout(layout); err != nil {
		return err
	}
	if !apply {
		return c.check(ctx, layout, out)
	}
	if c.loadRegistry == nil || c.loadLock == nil || c.pullImage == nil || c.writeLock == nil || c.withLock == nil {
		return errors.New("native WSL image lock mutation is incomplete")
	}
	return c.withLock(layout.RegistryPath, func() error {
		if err := c.validateLayout(layout); err != nil {
			return fmt.Errorf("revalidate native WSL layout under mutation lock: %w", err)
		}
		machinePolicy, err := c.loadPolicy()
		if err != nil {
			return fmt.Errorf("load native WSL machine policy: %w", err)
		}
		reg, path, err := c.loadRegistry(layout.RegistryPath, machinePolicy.AuthenticateRegistry)
		if err != nil {
			return fmt.Errorf("load or recover native WSL registry: %w", err)
		}
		if path != layout.RegistryPath {
			return fmt.Errorf("native WSL registry loader returned path %q, expected %q", path, layout.RegistryPath)
		}
		// Force validation/recovery of an interrupted prior write before a new
		// complete refresh. A malformed backup is never silently overwritten.
		if _, err := c.loadLock(layout.LockPath); err != nil {
			return fmt.Errorf("load or recover native WSL lockfile: %w", err)
		}
		localImages, err := localImageSet(reg, localTools)
		if err != nil {
			return err
		}
		images := lockfile.ConfiguredImages(reg)
		for _, image := range images {
			local := localImages[image]
			if err := machinePolicy.AuthorizeLockTarget(image, local); err != nil {
				return err
			}
			if local {
				continue
			}
			_, trustRequired, err := machinePolicy.ImageTrustFor(image)
			if err != nil {
				return fmt.Errorf("select native WSL image trust policy for %q: %w", image, err)
			}
			if trustRequired {
				return fmt.Errorf("native WSL lock apply cannot yet produce required image-signature evidence for %q; provision a validated private lockfile or use the supported Windows lock workflow", image)
			}
		}

		result := &lockfile.LockFile{Version: 1, Images: map[string]lockfile.LockEntry{}}
		for _, image := range images {
			if localImages[image] {
				fmt.Fprintf(out, "inspecting local %s\n", image)
			} else {
				fmt.Fprintf(out, "pulling  %s\n", image)
				if err := c.pullImage(ctx, image); err != nil {
					return err
				}
			}
			identity, err := c.inspectImage(ctx, image)
			if err != nil {
				return err
			}
			var entry lockfile.LockEntry
			if localImages[image] {
				entry, err = lockfile.ResolveLocalImageFromInspection(image, identity.id, identity.repoDigests, machinePolicy)
			} else {
				entry, err = lockfile.ResolveRepositoryImageFromInspection(image, identity.id, identity.repoDigests, machinePolicy)
			}
			if err != nil {
				return err
			}
			result.Images[image] = entry
			fmt.Fprintf(out, "  -> %s\n", entry.Resolved)
		}
		if err := c.writeLock(layout.LockPath, result, 0o600); err != nil {
			return fmt.Errorf("write native WSL lockfile: %w", err)
		}
		if err := c.checkLockRecovery(layout); err != nil {
			return fmt.Errorf("revalidate native WSL lockfile identity after write: %w", err)
		}
		fmt.Fprintf(out, "native WSL image lock applied and revalidated: %s\n", layout.LockPath)
		return nil
	})
}

func (c command) validateLayout(layout hostenv.WSLLayout) error {
	checked, err := c.checkLayout(layout)
	if err != nil {
		return fmt.Errorf("validate native WSL layout: %w", err)
	}
	if checked.Layout != layout || len(checked.MissingDirectories) != 0 {
		return errors.New("native WSL installation layout is incomplete; run `cb wsl install --apply`")
	}
	if err := c.checkRegistryRecovery(layout); err != nil {
		return fmt.Errorf("validate native WSL registry recovery state: %w", err)
	}
	if err := c.checkLockRecovery(layout); err != nil {
		return fmt.Errorf("validate native WSL lockfile recovery state: %w", err)
	}
	return nil
}

func (c command) check(ctx context.Context, layout hostenv.WSLLayout, out io.Writer) error {
	machinePolicy, err := c.loadPolicy()
	if err != nil {
		return fmt.Errorf("load native WSL machine policy: %w", err)
	}
	reg, path, err := c.loadRegistryReadOnly(layout.RegistryPath, machinePolicy.AuthenticateRegistry)
	if err != nil {
		return fmt.Errorf("load native WSL registry read-only: %w", err)
	}
	if path != layout.RegistryPath {
		return fmt.Errorf("native WSL registry loader returned path %q, expected %q", path, layout.RegistryPath)
	}
	locked, err := c.loadLockReadOnly(layout.LockPath)
	if err != nil {
		return fmt.Errorf("load native WSL lockfile read-only: %w", err)
	}
	if locked == nil {
		return fmt.Errorf("native WSL lockfile missing: %s (run `cb wsl lock --apply`)", layout.LockPath)
	}
	type candidate struct {
		image string
		entry lockfile.LockEntry
	}
	var candidates []candidate
	failures := 0
	for _, image := range lockfile.ConfiguredImages(reg) {
		entry, ok := locked.Images[image]
		if !ok || entry.Configured != image {
			fmt.Fprintf(out, "MISSING  %s\n", image)
			failures++
			continue
		}
		if err := machinePolicy.AuthorizeResolvedImage(image, entry.Resolved, lockfile.IsLocalResolved(entry.Resolved), entry.RuntimeTrustEvidence()); err != nil {
			fmt.Fprintf(out, "DENIED   %s (%v)\n", image, err)
			failures++
			continue
		}
		candidates = append(candidates, candidate{image: image, entry: entry})
	}
	// The complete configured set must pass lock and policy authorization before
	// any accepted reference reaches Docker. A partial diagnostic must not turn
	// a failed preflight into partial Engine activity.
	if failures != 0 {
		return fmt.Errorf("native WSL lock check failed: %d image(s) missing, unlocked, or denied", failures)
	}
	for _, candidate := range candidates {
		if _, err := c.inspectImage(ctx, candidate.entry.Resolved); err != nil {
			fmt.Fprintf(out, "ABSENT   %s -> %s\n", candidate.image, candidate.entry.Resolved)
			failures++
		} else {
			fmt.Fprintf(out, "OK       %s -> %s\n", candidate.image, candidate.entry.Resolved)
		}
	}
	if failures != 0 {
		return fmt.Errorf("native WSL lock check failed: %d authorized image(s) unavailable", failures)
	}
	fmt.Fprintf(out, "native WSL image lock OK (read-only; no files changed): %s\n", layout.LockPath)
	return nil
}

func parseArgs(args []string) (apply bool, localTools map[string]bool, err error) {
	localTools = map[string]bool{}
	if len(args) == 1 && args[0] == "--check" {
		return false, localTools, nil
	}
	if len(args) == 0 || args[0] != "--apply" || (len(args)-1)%2 != 0 {
		return false, nil, errors.New(usage)
	}
	for i := 1; i < len(args); i += 2 {
		if args[i] != "--local" || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
			return false, nil, errors.New(usage)
		}
		localTools[strings.ToLower(args[i+1])] = true
	}
	return true, localTools, nil
}

func localImageSet(reg registry.Registry, localTools map[string]bool) (map[string]bool, error) {
	images := map[string]bool{}
	for name := range localTools {
		tool, ok := reg.Tools[name]
		if !ok {
			return nil, fmt.Errorf("tool %q not found; --local requires a concrete configured tool name", name)
		}
		images[tool.Image] = true
	}
	return images, nil
}
