// Package policy loads and enforces the administrator-owned machine policy.
// Policy is an authorization layer over the user's resolved configuration; it
// never supplies defaults or merges into the registry.
package policy

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AviBackToBlack/container-bin/internal/toml"
)

const SchemaVersion = 1

type Error struct {
	Code string
	Err  error
}

func (e *Error) Error() string { return fmt.Sprintf("[policy.%s] %v", e.Code, e.Err) }
func (e *Error) Unwrap() error { return e.Err }

func policyError(code, format string, args ...any) error {
	return &Error{Code: code, Err: fmt.Errorf(format, args...)}
}

type Policy struct {
	Path                string
	SchemaVersion       int
	RequireLock         bool
	AllowLocalImages    bool
	AllowedRepositories []string
	ExpiresAt           *time.Time
	Fingerprint         string
}

func Path() string {
	if runtime.GOOS == "windows" {
		return `C:\ProgramData\ContainerBin\policy.toml`
	}
	return "/etc/container-bin/policy.toml"
}

func (p Policy) Managed() bool { return p.SchemaVersion != 0 }

func (p Policy) Summary() string {
	if !p.Managed() {
		return "unmanaged (machine policy absent)"
	}
	expires := "none"
	if p.ExpiresAt != nil {
		expires = p.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("managed schema=%d require_lock=%t allow_local_images=%t allowed_repositories=%d expires=%s fingerprint=sha256:%s source=%s",
		p.SchemaVersion, p.RequireLock, p.AllowLocalImages, len(p.AllowedRepositories), expires, p.Fingerprint, p.Path)
}

func Load() (Policy, error) {
	return loadAt(Path(), verifyOwnership, time.Now())
}

func loadAt(path string, ownership func(string) error, now time.Time) (Policy, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Policy{}, nil
	}
	if err != nil {
		return Policy{}, policyError("unreadable", "inspect %s: %v", path, err)
	}
	if !info.Mode().IsRegular() {
		return Policy{}, policyError("ownership", "%s: policy must be a regular file", path)
	}
	if err := ownership(path); err != nil {
		return Policy{}, policyError("ownership", "%s: %v", path, err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Policy{}, policyError("unreadable", "read %s: %v", path, err)
	}
	p, err := parse(path, b, now)
	if err != nil {
		return Policy{}, err
	}
	return p, nil
}

func parse(path string, b []byte, now time.Time) (Policy, error) {
	p := Policy{Path: path}
	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(toml.StripComment(sc.Text()))
		if line == "" {
			continue
		}
		if section, ok, err := toml.ParseSectionHeader(line); err != nil {
			return Policy{}, policyError("syntax", "line %d: %v", lineNo, err)
		} else if ok {
			return Policy{}, policyError("syntax", "line %d: unsupported section %q", lineNo, section)
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			return Policy{}, policyError("syntax", "line %d: expected key = value", lineNo)
		}
		key, raw := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		if seen[key] {
			return Policy{}, policyError("syntax", "line %d: duplicate key %q", lineNo, key)
		}
		seen[key] = true
		switch key {
		case "policy_version":
			v, err := strconv.Atoi(raw)
			if err != nil {
				return Policy{}, policyError("version", "line %d: policy_version must be an integer", lineNo)
			}
			p.SchemaVersion = v
		case "require_lock":
			v, err := toml.ParseBool(raw)
			if err != nil {
				return Policy{}, policyError("syntax", "line %d require_lock: %v", lineNo, err)
			}
			p.RequireLock = v
		case "allow_local_images":
			v, err := toml.ParseBool(raw)
			if err != nil {
				return Policy{}, policyError("syntax", "line %d allow_local_images: %v", lineNo, err)
			}
			p.AllowLocalImages = v
		case "allowed_repositories":
			values, err := toml.ParseStringArray(raw)
			if err != nil {
				return Policy{}, policyError("syntax", "line %d allowed_repositories: %v", lineNo, err)
			}
			p.AllowedRepositories = values
		case "expires_at":
			value, err := toml.ParseQuoted(raw)
			if err != nil {
				return Policy{}, policyError("syntax", "line %d expires_at: %v", lineNo, err)
			}
			expires, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return Policy{}, policyError("syntax", "line %d expires_at must be RFC3339: %v", lineNo, err)
			}
			p.ExpiresAt = &expires
		default:
			return Policy{}, policyError("syntax", "line %d: unsupported key %q", lineNo, key)
		}
	}
	if err := sc.Err(); err != nil {
		return Policy{}, policyError("unreadable", "scan %s: %v", path, err)
	}
	if p.SchemaVersion != SchemaVersion {
		return Policy{}, policyError("version", "unsupported policy_version %d (supported: %d)", p.SchemaVersion, SchemaVersion)
	}
	if !p.RequireLock && len(p.AllowedRepositories) == 0 {
		return Policy{}, policyError("syntax", "policy has no authorization controls")
	}
	if p.ExpiresAt != nil && !now.Before(*p.ExpiresAt) {
		return Policy{}, policyError("expired", "policy expired at %s", p.ExpiresAt.UTC().Format(time.RFC3339))
	}

	canonical := make([]string, 0, len(p.AllowedRepositories))
	set := map[string]bool{}
	for _, rule := range p.AllowedRepositories {
		rule, err := canonicalRule(rule)
		if err != nil {
			return Policy{}, policyError("syntax", "invalid allowed_repositories entry %q: %v", rule, err)
		}
		if !set[rule] {
			set[rule] = true
			canonical = append(canonical, rule)
		}
	}
	sort.Strings(canonical)
	p.AllowedRepositories = canonical
	sum := sha256.Sum256(b)
	p.Fingerprint = hex.EncodeToString(sum[:])
	return p, nil
}

func (p Policy) AuthorizeImage(configured string, locked, local bool) error {
	if !p.Managed() {
		return nil
	}
	if p.RequireLock && !locked {
		return policyError("lock_required", "image %q is not covered by an exact lock entry", configured)
	}
	if local {
		if !p.AllowLocalImages {
			return policyError("local_image_denied", "local image %q has no authorized registry origin", configured)
		}
		return nil
	}
	if len(p.AllowedRepositories) == 0 {
		return nil
	}
	_, err := p.authorizeRepository(configured)
	if err != nil {
		return policyError("repository_denied", "image %q: %v", configured, err)
	}
	return nil
}

// AuthorizeResolvedImage checks both sides of a lock entry. The configured
// reference alone is not sufficient proof of origin because a hand-edited
// lockfile could point its resolved digest at another repository.
func (p Policy) AuthorizeResolvedImage(configured, resolved string, local bool) error {
	if err := p.AuthorizeImage(configured, true, local); err != nil {
		return err
	}
	if local || !p.Managed() || len(p.AllowedRepositories) == 0 {
		return nil
	}
	if _, err := p.authorizeRepository(resolved); err != nil {
		return policyError("repository_denied", "resolved lock reference %q is not authorized: %v", resolved, err)
	}
	return nil
}

func (p Policy) authorizeRepository(ref string) (string, error) {
	repo, err := CanonicalRepository(ref)
	if err != nil {
		return "", err
	}
	for _, allowed := range p.AllowedRepositories {
		if repo == allowed || strings.HasPrefix(repo, allowed+"/") {
			return repo, nil
		}
	}
	return repo, fmt.Errorf("repository %q is outside the allowed repository boundaries", repo)
}

// AuthorizeLockTarget checks whether policy permits creating or refreshing a
// lock entry. It intentionally omits the runtime require_lock check because the
// operation itself is what establishes that lock.
func (p Policy) AuthorizeLockTarget(configured string, local bool) error {
	if !p.Managed() {
		return nil
	}
	copy := p
	copy.RequireLock = false
	return copy.AuthorizeImage(configured, true, local)
}

// CanonicalRepository returns the registry-qualified repository without a tag
// or digest. Docker Hub aliases and implicit references are normalized.
func CanonicalRepository(ref string) (string, error) {
	ref = strings.TrimSpace(strings.ToLower(ref))
	if strings.HasPrefix(ref, "sha256:") {
		return "", errors.New("local image IDs do not have a registry repository")
	}
	if ref == "" || strings.ContainsAny(ref, "\\ 	\r\n") || strings.Contains(ref, "://") || strings.HasPrefix(ref, "/") {
		return "", errors.New("invalid image reference")
	}
	if strings.Count(ref, "@") > 1 || strings.HasPrefix(ref, "@") || strings.HasSuffix(ref, "@") {
		return "", errors.New("invalid image digest")
	}
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		ref = ref[:i]
	}
	lastSlash := strings.LastIndexByte(ref, '/')
	if colon := strings.LastIndexByte(ref, ':'); colon > lastSlash {
		ref = ref[:colon]
	}
	parts := strings.Split(ref, "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("invalid repository path")
		}
		if i > 0 && strings.Contains(part, ":") {
			return "", errors.New("tag separator is only valid at the end of an image reference")
		}
	}
	first := parts[0]
	qualified := strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost"
	if qualified {
		if err := validateRegistryHost(first); err != nil {
			return "", err
		}
	}
	if !qualified {
		if len(parts) == 1 {
			parts = []string{"docker.io", "library", first}
		} else {
			parts = append([]string{"docker.io"}, parts...)
		}
	} else {
		switch first {
		case "index.docker.io", "registry-1.docker.io":
			parts[0] = "docker.io"
		}
		if parts[0] == "docker.io" && len(parts) == 2 {
			parts = []string{"docker.io", "library", parts[1]}
		}
	}
	if len(parts) < 2 {
		return "", errors.New("repository name is required")
	}
	return strings.Join(parts, "/"), nil
}

func canonicalRule(rule string) (string, error) {
	rule = strings.TrimSpace(strings.ToLower(rule))
	if rule == "" || strings.ContainsAny(rule, "@\\ 	\r\n") || strings.Contains(rule, "://") || strings.HasPrefix(rule, "/") || strings.HasSuffix(rule, "/") {
		return "", errors.New("invalid repository boundary")
	}
	parts := strings.Split(rule, "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("invalid repository boundary")
		}
		if i > 0 && strings.Contains(part, ":") {
			return "", errors.New("repository boundaries cannot contain tags")
		}
	}
	if strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":") || parts[0] == "localhost" {
		if err := validateRegistryHost(parts[0]); err != nil {
			return "", err
		}
	}
	switch parts[0] {
	case "index.docker.io", "registry-1.docker.io":
		parts[0] = "docker.io"
	}
	if len(parts) == 1 && !strings.Contains(parts[0], ".") && !strings.Contains(parts[0], ":") && parts[0] != "localhost" {
		parts = []string{"docker.io", parts[0]}
	}
	return strings.Join(parts, "/"), nil
}

func validateRegistryHost(host string) error {
	if strings.ContainsAny(host, "[]") {
		return errors.New("IPv6 registry hosts are not supported in policy schema 1")
	}
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		name, port := host[:i], host[i+1:]
		if name == "" || port == "" {
			return errors.New("invalid registry host or port")
		}
		for _, r := range port {
			if r < '0' || r > '9' {
				return errors.New("registry port must be numeric")
			}
		}
	}
	return nil
}
