// Package selfupdate implements release selection, same-volume staging and
// artifact verification for ContainerBin's transactional self-update.
// Installed-file replacement remains a deliberately separate later phase.
package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	apiRoot         = "https://api.github.com/repos/AviBackToBlack/container-bin"
	releaseWebRoot  = "https://github.com/AviBackToBlack/container-bin/releases"
	apiVersion      = "2026-03-10"
	maxResponseSize = 1 << 20
	maxBinarySize   = 100 << 20
	maxArchiveSize  = 100 << 20
	maxChecksumSize = 64 << 10
)

type Options struct {
	Check          bool
	Prerelease     bool
	Version        string
	AllowDowngrade bool
}

type Asset struct {
	Name string
	URL  string
	Size int64
}

type Plan struct {
	Current                string
	Target                 string
	Channel                string
	Status                 string
	OS                     string
	Arch                   string
	ReleaseURL             string
	Binary                 Asset
	Archive                Asset
	Checksums              Asset
	ExpectedRepo           string
	ExpectedRef            string
	Workflow               string
	downgradeAuthorization downgradeAuthorization
	checksumLayout         checksumLayout
}

type checksumLayout uint8

const (
	checksumLayoutLegacyAMD64 checksumLayout = iota + 1
	checksumLayoutDualArch
)

type downgradeAuthorization struct {
	current string
	target  string
}

type releaseAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type release struct {
	TagName    string         `json:"tag_name"`
	HTMLURL    string         `json:"html_url"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Assets     []releaseAsset `json:"assets"`
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type checker struct {
	doer httpDoer
}

func ParseArgs(args []string) (Options, error) {
	var opts Options
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--check":
			if opts.Check {
				return Options{}, errors.New("--check may be specified only once")
			}
			opts.Check = true
		case "--prerelease":
			if opts.Prerelease {
				return Options{}, errors.New("--prerelease may be specified only once")
			}
			opts.Prerelease = true
		case "--version":
			if opts.Version != "" || i+1 == len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return Options{}, errors.New("--version requires one canonical version")
			}
			i++
			opts.Version = args[i]
		case "--allow-downgrade":
			if opts.AllowDowngrade {
				return Options{}, errors.New("--allow-downgrade may be specified only once")
			}
			opts.AllowDowngrade = true
		default:
			return Options{}, fmt.Errorf("unknown self-update option %q", args[i])
		}
	}
	if !opts.Check {
		return Options{}, errors.New("this build implements only the read-only selection phase; use `cb self-update --check`")
	}
	if opts.Prerelease && opts.Version != "" {
		return Options{}, errors.New("--prerelease and --version are mutually exclusive")
	}
	if opts.Version != "" {
		if _, err := parseVersion(opts.Version); err != nil {
			return Options{}, err
		}
	}
	return opts, nil
}

func Check(ctx context.Context, current string, args []string, out io.Writer) error {
	opts, err := ParseArgs(args)
	if err != nil {
		return err
	}
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	plan, err := (checker{doer: client}).Plan(ctx, current, runtime.GOOS, runtime.GOARCH, opts)
	if err != nil {
		return err
	}
	printPlan(out, plan)
	return nil
}

func (c checker) Plan(ctx context.Context, current, goos, goarch string, opts Options) (Plan, error) {
	currentParsed, err := currentVersion(current)
	if err != nil {
		return Plan{}, err
	}
	if goos != "windows" || (goarch != "amd64" && goarch != "arm64") {
		return Plan{}, fmt.Errorf("self-update has no qualified artifact for %s/%s (supported: windows/amd64, windows/arm64)", goos, goarch)
	}
	selected, channel, err := c.selectRelease(ctx, current, opts)
	if err != nil {
		return Plan{}, err
	}
	target, err := parseVersion(selected.TagName)
	if err != nil {
		return Plan{}, fmt.Errorf("selected release has invalid tag: %w", err)
	}
	comparison := currentParsed.compare(target)
	status := "UPDATE AVAILABLE"
	switch {
	case comparison == 0:
		status = "CURRENT"
	case comparison > 0 && !opts.AllowDowngrade:
		return Plan{}, fmt.Errorf("selected target %s is older than current %s; pass --allow-downgrade explicitly", target.raw, currentParsed.raw)
	case comparison > 0:
		status = "DOWNGRADE AUTHORIZED (CHECK ONLY)"
	}
	binary, archive, checksums, layout, err := validateRelease(selected, goarch)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{
		Current:        currentParsed.raw,
		Target:         target.raw,
		Channel:        channel,
		Status:         status,
		OS:             goos,
		Arch:           goarch,
		ReleaseURL:     selected.HTMLURL,
		Binary:         binary,
		Archive:        archive,
		Checksums:      checksums,
		ExpectedRepo:   "AviBackToBlack/container-bin",
		ExpectedRef:    "refs/tags/" + target.raw,
		Workflow:       ".github/workflows/release.yml",
		checksumLayout: layout,
	}
	if comparison > 0 && opts.AllowDowngrade {
		plan.downgradeAuthorization = downgradeAuthorization{current: currentParsed.raw, target: target.raw}
	}
	return plan, nil
}

func (c checker) selectRelease(ctx context.Context, current string, opts Options) (release, string, error) {
	switch {
	case opts.Version != "":
		var selected release
		if err := c.getJSON(ctx, apiRoot+"/releases/tags/"+opts.Version, current, &selected); err != nil {
			return release{}, "", err
		}
		if selected.Draft || selected.TagName != opts.Version {
			return release{}, "", fmt.Errorf("exact release response does not identify published target %s", opts.Version)
		}
		return selected, "exact", nil
	case opts.Prerelease:
		var releases []release
		if err := c.getJSON(ctx, apiRoot+"/releases?per_page=30", current, &releases); err != nil {
			return release{}, "", err
		}
		var candidates []struct {
			release release
			version semanticVersion
		}
		for _, candidate := range releases {
			if candidate.Draft || !candidate.Prerelease {
				continue
			}
			v, err := parseVersion(candidate.TagName)
			if err != nil || len(v.prerelease) == 0 {
				continue
			}
			candidates = append(candidates, struct {
				release release
				version semanticVersion
			}{candidate, v})
		}
		if len(candidates) == 0 {
			return release{}, "", errors.New("no published canonical prerelease is available")
		}
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].version.compare(candidates[j].version) > 0 })
		return candidates[0].release, "prerelease", nil
	default:
		var selected release
		if err := c.getJSON(ctx, apiRoot+"/releases/latest", current, &selected); err != nil {
			return release{}, "", err
		}
		v, err := parseVersion(selected.TagName)
		if selected.Draft || selected.Prerelease || err != nil || len(v.prerelease) != 0 {
			return release{}, "", errors.New("latest stable endpoint returned a draft, prerelease, or invalid tag")
		}
		return selected, "stable", nil
	}
}

func (c checker) getJSON(ctx context.Context, endpoint, current string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "container-bin/"+current)
	endpointLabel := req.URL.RequestURI()
	resp, err := c.doer.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub release query %s failed: %w", endpointLabel, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub release query %s returned HTTP %d", endpointLabel, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return fmt.Errorf("read GitHub release response: %w", err)
	}
	if len(body) > maxResponseSize {
		return errors.New("GitHub release response exceeded the 1 MiB limit")
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("decode GitHub release response: %w", err)
	}
	return nil
}

func validateRelease(selected release, goarch string) (Asset, Asset, Asset, checksumLayout, error) {
	tag, err := parseVersion(selected.TagName)
	if err != nil {
		return Asset{}, Asset{}, Asset{}, 0, err
	}
	wantReleaseURL := releaseWebRoot + "/tag/" + tag.raw
	if selected.HTMLURL != wantReleaseURL {
		return Asset{}, Asset{}, Asset{}, 0, fmt.Errorf("release URL %q is outside the canonical release page", selected.HTMLURL)
	}
	amd64Archive := fmt.Sprintf("container-bin-%s-windows-amd64.zip", tag.raw)
	arm64Archive := fmt.Sprintf("container-bin-%s-windows-arm64.zip", tag.raw)
	wanted := map[string]int64{
		"cb.exe":     maxBinarySize,
		amd64Archive: maxArchiveSize,
		arm64Archive: maxArchiveSize,
		"SHA256SUMS": maxChecksumSize,
	}
	found := map[string]Asset{}
	for _, candidate := range selected.Assets {
		limit, required := wanted[candidate.Name]
		if !required {
			continue
		}
		if _, duplicate := found[candidate.Name]; duplicate {
			return Asset{}, Asset{}, Asset{}, 0, fmt.Errorf("release contains duplicate required asset %q", candidate.Name)
		}
		if candidate.Size <= 0 || candidate.Size > limit {
			return Asset{}, Asset{}, Asset{}, 0, fmt.Errorf("release asset %q has invalid size %d (limit %d)", candidate.Name, candidate.Size, limit)
		}
		wantURL := releaseWebRoot + "/download/" + tag.raw + "/" + candidate.Name
		if candidate.BrowserDownloadURL != wantURL {
			return Asset{}, Asset{}, Asset{}, 0, fmt.Errorf("release asset %q has non-canonical download URL", candidate.Name)
		}
		found[candidate.Name] = Asset{Name: candidate.Name, URL: candidate.BrowserDownloadURL, Size: candidate.Size}
	}
	for _, name := range []string{"cb.exe", amd64Archive, "SHA256SUMS"} {
		if _, ok := found[name]; !ok {
			return Asset{}, Asset{}, Asset{}, 0, fmt.Errorf("release is missing required asset %q", name)
		}
	}
	layout := checksumLayoutLegacyAMD64
	if _, ok := found[arm64Archive]; ok {
		layout = checksumLayoutDualArch
	}
	if goarch == "arm64" && layout != checksumLayoutDualArch {
		return Asset{}, Asset{}, Asset{}, 0, fmt.Errorf("release is missing required asset %q", arm64Archive)
	}
	if goarch == "arm64" {
		return found[arm64Archive], found[arm64Archive], found["SHA256SUMS"], layout, nil
	}
	return found["cb.exe"], found[amd64Archive], found["SHA256SUMS"], layout, nil
}

func printPlan(out io.Writer, plan Plan) {
	fmt.Fprintln(out, "self-update check (read-only; no files changed)")
	fmt.Fprintf(out, "current:      %s\n", plan.Current)
	fmt.Fprintf(out, "target:       %s\n", plan.Target)
	fmt.Fprintf(out, "channel:      %s\n", plan.Channel)
	fmt.Fprintf(out, "status:       %s\n", plan.Status)
	fmt.Fprintf(out, "platform:     %s/%s\n", plan.OS, plan.Arch)
	fmt.Fprintf(out, "release:      %s\n", plan.ReleaseURL)
	fmt.Fprintf(out, "artifact:     %s (%d bytes)\n", plan.Binary.Name, plan.Binary.Size)
	if plan.Archive.Name != plan.Binary.Name {
		fmt.Fprintf(out, "archive:      %s (%d bytes)\n", plan.Archive.Name, plan.Archive.Size)
	}
	fmt.Fprintf(out, "checksums:    %s (%d bytes)\n", plan.Checksums.Name, plan.Checksums.Size)
	fmt.Fprintf(out, "verification: gh attestation verify; repository=%s workflow=%s ref=%s; checksums additionally required; no fallback\n", plan.ExpectedRepo, plan.Workflow, plan.ExpectedRef)
	fmt.Fprintln(out, "apply:        unavailable in this slice; verified download and transactional replacement are separate roadmap phases")
}
