// Package aclaudit implements a read-only, limited Windows ACL candidate audit.
// It does not compute effective access or change execution authorization.
package aclaudit

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxObjects    = 256
	maxDepth      = 64
	maxACEs       = 4096
	maxLines      = 24
	maxDescriptor = 65536
	auditTimeout  = 8 * time.Second
)

type Asset struct{ Path, Label string }
type Request struct {
	Directory      string
	Assets         []Asset
	OptionalAssets []Asset
}
type Diagnostic struct{ Status, Message string }
type ace struct {
	Type, Flags byte
	Mask        uint32
	SID         string
}
type descriptor struct {
	Owner    string
	Control  uint16
	NullDACL bool
	ACEs     []ace
	Hash     [32]byte
}
type snapshot struct {
	Identity   string
	Resolved   string
	Filesystem string
	Directory  bool
	Reparse    uint32
	Security   descriptor
}
type scope struct {
	Updater, Profile string
	Qualified        bool
	Anchor           snapshot
}
type reader func(string) (snapshot, error)

type Report struct {
	findings   []string
	incomplete []string
	objects    int
	candidates int
}

func (r *Report) missing(reason string) {
	for _, old := range r.incomplete {
		if old == reason {
			return
		}
	}
	if len(r.incomplete) < maxLines {
		r.incomplete = append(r.incomplete, reason)
	}
}
func (r *Report) candidate(reason string) {
	r.candidates++
	if len(r.findings) < maxLines-1 {
		r.findings = append(r.findings, reason)
	} else {
		r.missing("candidate output limit reached")
	}
}
func (r Report) copy() Report {
	r.findings = append([]string(nil), r.findings...)
	r.incomplete = append([]string(nil), r.incomplete...)
	return r
}
func (r Report) Diagnostics() []Diagnostic {
	r = r.copy()
	if r.objects == 0 && len(r.incomplete) == 0 {
		r.missing("no objects inspected")
	}
	lines := []Diagnostic{}
	sort.Strings(r.findings)
	sort.Strings(r.incomplete)
	for _, finding := range r.findings {
		if len(lines) >= maxLines-1 {
			break
		}
		lines = append(lines, Diagnostic{"warn", "Windows ACL candidate: " + finding + "; effective access and replacement exploitability are not established"})
	}
	if len(r.incomplete) != 0 {
		// A fixed aggregate keeps all coverage failures visible without disclosing
		// raw errors/absolute paths or allowing thousands of output lines.
		for _, reason := range r.incomplete {
			if len(lines) >= maxLines-1 {
				break
			}
			lines = append(lines, Diagnostic{"warn", "ancestor ACL coverage incomplete: " + reason})
		}
		lines = append(lines, Diagnostic{"warn", fmt.Sprintf("ancestor ACL coverage incomplete: limited Windows ACL scan inspected %d objects and observed %d candidate findings; review the installation trust boundary; no security certification", r.objects, r.candidates)})
	} else if r.candidates != 0 {
		lines = append(lines, Diagnostic{"warn", fmt.Sprintf("limited Windows ACL scan: %d candidate findings within the updater/SYSTEM/Administrators model; no effective-access or future-race guarantee", r.candidates)})
	} else {
		lines = append(lines, Diagnostic{"ok", "limited Windows ACL scan: no candidate authority found within the declared updater/SYSTEM/Administrators model; effective access for all identities and future races are not certified"})
	}
	return lines
}

func trusted(sid, updater string) bool {
	return sid != "" && (sid == updater || sid == "S-1-5-18" || sid == "S-1-5-32-544")
}
func knownIdentity(sid, updater string) bool {
	if trusted(sid, updater) {
		return true
	}
	switch sid {
	case "S-1-1-0", "S-1-5-11", "S-1-5-32-545", "S-1-5-19", "S-1-5-20", "S-1-5-4", "S-1-5-7":
		return true
	}
	return false
}

// A native profile path and matching object owner can validate the caller only
// for this redirection object. This does not qualify its target installation.
func classifyRedirection(r *Report, label string, node snapshot, caller string) {
	updater := ""
	if caller != "" && node.Security.Owner == caller {
		updater = caller
	}
	classify(r, label, node, updater)
	r.missing("redirection object/target chain is not qualified")
}

// Deliberately never subtract deny ACEs. These are conservative candidates,
// not Windows AccessCheck results. Inherit-only grants do not apply here.
func classify(r *Report, label string, node snapshot, updater string) {
	d := node.Security
	if !trusted(d.Owner, updater) {
		r.candidate(label + ": owner SID " + d.Owner + " may control the DACL (owner-rights restrictions are not evaluated)")
		if !knownIdentity(d.Owner, updater) {
			r.missing(label + ": owner identity outside qualified model")
		}
	}
	if d.NullDACL {
		r.candidate(label + ": null/absent DACL permits broad discretionary access")
		r.missing(label + ": null/absent DACL")
		return
	}
	for index, a := range d.ACEs {
		const knownMask uint32 = 0xf01f01ff
		if a.Mask & ^knownMask != 0 {
			r.missing(label + ": unsupported access-mask bits")
		}
		if a.Type != 0 && a.Type != 1 {
			r.missing(fmt.Sprintf("%s: ACE[%d] unsupported type=0x%02x flags=0x%02x mask=0x%08x", label, index, a.Type, a.Flags, a.Mask))
			continue
		}
		if a.Flags & ^byte(0x1f) != 0 {
			r.missing(fmt.Sprintf("%s: ACE[%d] unsupported flags=0x%02x", label, index, a.Flags))
		}
		if a.Flags&8 != 0 {
			continue
		}
		if !knownIdentity(a.SID, updater) {
			r.missing(label + ": ACE SID " + a.SID + " outside qualified identity model")
		}
		if a.Type != 0 || trusted(a.SID, updater) {
			continue
		}
		var rights []string
		if a.Mask&(0x40000|0x10000000) != 0 {
			rights = append(rights, "WRITE_DAC")
		}
		if a.Mask&(0x80000|0x10000000) != 0 {
			rights = append(rights, "WRITE_OWNER")
		}
		if a.Mask&(0x10000|0x10000000) != 0 {
			rights = append(rights, "object DELETE (not parent DELETE_CHILD)")
		}
		if node.Directory {
			if a.Mask&(0x40|0x10000000) != 0 {
				rights = append(rights, "parent DELETE_CHILD (child deletion candidate)")
			}
			if a.Mask&(6|0x40000000|0x10000000) != 0 {
				rights = append(rights, "child creation only; replacement not established")
			}
		} else if a.Mask&(6|0x40000000|0x10000000) != 0 {
			rights = append(rights, "file content write/append")
		}
		if a.Mask&(0x110|0x40000000|0x10000000) != 0 {
			rights = append(rights, "attributes/extended-attributes write")
		}
		if len(rights) != 0 {
			r.candidate(fmt.Sprintf("%s: ACE[%d] SID %s type=0x%02x flags=0x%02x mask=0x%08x: %s", label, index, a.SID, a.Type, a.Flags, a.Mask, strings.Join(rights, ", ")))
		}
	}
}

func within(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Paths are inspected root-first so an intermediate redirection is observed
// before any descendant is opened. Unqualified redirects are never followed.
func run(ctx context.Context, req Request, s scope, read reader, progress func(Report)) Report {
	var report Report
	if !s.Qualified || s.Updater == "" || !filepath.IsAbs(s.Profile) || !within(req.Directory, s.Profile) || filepath.Clean(req.Directory) == filepath.Clean(s.Profile) {
		report.missing("installation is not a qualified per-user scope; machine-wide/custom managers are not declared")
		return report
	}
	if len(req.Assets)+len(req.OptionalAssets) > maxObjects {
		report.missing("managed-asset count limit reached")
		return report
	}
	paths := append([]Asset{{req.Directory, "installation"}}, req.Assets...)
	paths = append(paths, req.OptionalAssets...)
	optional := map[string]bool{}
	for _, asset := range req.OptionalAssets {
		optional[strings.ToLower(filepath.Clean(asset.Path))] = true
	}
	absent := map[string]Asset{}
	seen := map[string]snapshot{}
	labels := map[string]string{}
	blocked := map[string]bool{}
	aceCount := 0
	for targetIndex, target := range paths {
		if !validLabel(target.Label) {
			report.missing("invalid asset diagnostic label")
			continue
		}
		if !filepath.IsAbs(target.Path) || !within(target.Path, req.Directory) {
			report.missing("managed asset outside qualified installation")
			continue
		}
		path := filepath.Clean(target.Path)
		var chain []string
		for n := 0; ; n++ {
			if n >= maxDepth {
				report.missing("ancestor depth limit reached")
				chain = nil
				break
			}
			chain = append(chain, path)
			parent := filepath.Dir(path)
			if parent == path {
				break
			}
			path = parent
		}
		for i := len(chain) - 1; i >= 0; i-- {
			if ctx.Err() != nil {
				report.missing("audit time limit reached")
				return report
			}
			path = chain[i]
			key := strings.ToLower(path)
			if blocked[key] {
				break
			}
			if _, ok := seen[key]; ok {
				continue
			}
			if len(seen) >= maxObjects {
				report.missing("object count limit reached")
				return report
			}
			label := fmt.Sprintf("%s ancestor[%d]", target.Label, i)
			if i == 0 {
				label = target.Label
			}
			node, err := read(path)
			if err != nil {
				if i == 0 && optional[key] && errors.Is(err, fs.ErrNotExist) {
					absent[key] = target
					blocked[key] = true
					break
				}
				report.missing(label + ": native metadata/security inspection failed")
				blocked[key] = true
				break
			}
			report.objects++
			seen[key] = node
			labels[key] = label
			aceCount += len(node.Security.ACEs)
			if aceCount > maxACEs {
				report.missing("ACE count limit reached")
				return report
			}
			classify(&report, label, node, s.Updater)
			if strings.EqualFold(path, filepath.Clean(req.Directory)) && node.Security.Owner != s.Updater {
				report.missing("installation owner does not validate the updater SID")
			}
			if strings.EqualFold(path, filepath.Clean(req.Directory)) && s.Anchor.Identity != "" && !sameSnapshot(s.Anchor, node) {
				report.missing("installation identity/security changed after updater qualification")
			}
			if node.Filesystem != "NTFS" {
				report.missing(label + ": filesystem is not qualified local NTFS")
				blocked[key] = true
			}
			if node.Reparse != 0 {
				report.missing(label + ": redirection object/target chain not qualified")
				blocked[key] = true
			}
			if !strings.EqualFold(filepath.Clean(node.Resolved), path) {
				report.missing(label + ": resolved path identity differs; target chain not qualified")
				blocked[key] = true
			}
			if i > 0 && !node.Directory {
				report.missing(label + ": ancestor is not a directory")
				blocked[key] = true
			}
			if i == 0 && targetIndex > 0 && node.Directory {
				report.missing(label + ": managed file is a directory")
				blocked[key] = true
			}
			if node.Identity == "" {
				report.missing(label + ": native object identity unavailable")
				blocked[key] = true
			}
			progress(report.copy())
			if blocked[key] {
				break
			}
		}
	}
	// Re-open in deterministic order, detecting sampled identity/DACL changes.
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if ctx.Err() != nil {
			report.missing("audit time limit reached")
			break
		}
		node, err := read(seen[key].Resolved)
		if err != nil || !sameSnapshot(seen[key], node) {
			report.missing(labels[key] + ": object identity or security descriptor changed during inspection")
		}
		progress(report.copy())
	}
	keys = keys[:0]
	for key := range absent {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if ctx.Err() != nil {
			report.missing("audit time limit reached")
			break
		}
		if report.objects >= maxObjects {
			report.missing("object count limit reached")
			break
		}
		node, err := read(absent[key].Path)
		if err == nil {
			report.objects++
			aceCount += len(node.Security.ACEs)
			if aceCount > maxACEs {
				report.missing("ACE count limit reached")
				break
			}
			classify(&report, absent[key].Label, node, s.Updater)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			report.missing(absent[key].Label + ": optional asset appeared or inspection failed during recheck")
		}
		progress(report.copy())
	}
	if report.objects == 0 {
		report.missing("no objects inspected")
	}
	return report
}

func validLabel(label string) bool {
	if len(label) == 0 || len(label) > 80 {
		return false
	}
	for _, c := range label {
		if c < 32 || c > 126 {
			return false
		}
	}
	return true
}

func sameSnapshot(a, b snapshot) bool {
	if a.Identity != b.Identity || !strings.EqualFold(a.Resolved, b.Resolved) || a.Filesystem != b.Filesystem || a.Directory != b.Directory || a.Reparse != b.Reparse || a.Security.Owner != b.Security.Owner || a.Security.Control != b.Security.Control || a.Security.NullDACL != b.Security.NullDACL || a.Security.Hash != b.Security.Hash || len(a.Security.ACEs) != len(b.Security.ACEs) {
		return false
	}
	for i := range a.Security.ACEs {
		if a.Security.ACEs[i] != b.Security.ACEs[i] {
			return false
		}
	}
	return true
}

var workerSlot = make(chan struct{}, 1)

// A stalled native metadata call must not hang doctor or accumulate workers.
// The bounded worker owns its input and report; progress snapshots preserve
// evidence already observed even if the caller deadline expires during an API.
func bounded(ctx context.Context, job func(context.Context, func(Report)) Report) Report {
	select {
	case workerSlot <- struct{}{}:
	default:
		var r Report
		r.missing("a previous native audit is still pending")
		return r
	}
	var mu sync.Mutex
	var latest Report
	done := make(chan Report, 1)
	go func() {
		r := func() Report {
			defer func() { <-workerSlot }()
			return job(ctx, func(r Report) { mu.Lock(); latest = r.copy(); mu.Unlock() })
		}()
		done <- r
	}()
	select {
	case r := <-done:
		return r
	case <-ctx.Done():
		mu.Lock()
		r := latest.copy()
		mu.Unlock()
		r.missing("audit time limit reached; native I/O may finish later")
		return r
	}
}
