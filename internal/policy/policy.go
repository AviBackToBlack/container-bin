// Package policy loads and enforces the administrator-owned machine policy.
// Policy is an authorization layer over the user's resolved configuration; it
// never supplies defaults or merges into the registry.
package policy

import (
	"bufio"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AviBackToBlack/container-bin/internal/toml"
)

const (
	MaxSchemaVersion             = 2
	registrySignatureVersion     = 1
	registrySignatureAlgorithm   = "ed25519"
	maxRegistrySignatureFileSize = 16 << 10
)

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
	Path                     string
	SchemaVersion            int
	RequireLock              bool
	AllowLocalImages         bool
	AllowedRepositories      []string
	RequireRegistrySignature bool
	ExpiresAt                *time.Time
	Fingerprint              string
	registrySigningKeys      map[string]registrySigningKey
	revokedRegistryKeyIDs    map[string]bool
}

type registrySigningKey struct {
	PublicKey ed25519.PublicKey
	NotBefore time.Time
	ExpiresAt time.Time
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
	return fmt.Sprintf("managed schema=%d require_lock=%t allow_local_images=%t allowed_repositories=%d require_registry_signature=%t registry_trusted_keys=%d registry_revoked_keys=%d expires=%s fingerprint=sha256:%s source=%s",
		p.SchemaVersion, p.RequireLock, p.AllowLocalImages, len(p.AllowedRepositories), p.RequireRegistrySignature, len(p.registrySigningKeys), len(p.revokedRegistryKeyIDs), expires, p.Fingerprint, p.Path)
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
	var registrySigningKeySpecs, revokedRegistryKeyIDs []string
	usedRegistrySignatureFields := false
	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	lineNo := 0
	for sc.Scan() {
		lineNo++
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
			startLine := lineNo
			for strings.HasPrefix(strings.TrimSpace(raw), "[") && !arrayValueComplete(raw) {
				if !sc.Scan() {
					if err := sc.Err(); err != nil {
						return Policy{}, policyError("unreadable", "scan %s: %v", path, err)
					}
					return Policy{}, policyError("syntax", "line %d allowed_repositories: unterminated array", startLine)
				}
				lineNo++
				raw += "\n" + strings.TrimSpace(toml.StripComment(sc.Text()))
			}
			values, err := toml.ParseStringArray(raw)
			if err != nil {
				return Policy{}, policyError("syntax", "line %d allowed_repositories: %v", startLine, err)
			}
			p.AllowedRepositories = values
		case "require_registry_signature":
			v, err := toml.ParseBool(raw)
			if err != nil {
				return Policy{}, policyError("syntax", "line %d require_registry_signature: %v", lineNo, err)
			}
			p.RequireRegistrySignature = v
			usedRegistrySignatureFields = true
		case "registry_signing_keys", "revoked_registry_key_ids":
			startLine := lineNo
			for strings.HasPrefix(strings.TrimSpace(raw), "[") && !arrayValueComplete(raw) {
				if !sc.Scan() {
					if err := sc.Err(); err != nil {
						return Policy{}, policyError("unreadable", "scan %s: %v", path, err)
					}
					return Policy{}, policyError("syntax", "line %d %s: unterminated array", startLine, key)
				}
				lineNo++
				raw += "\n" + strings.TrimSpace(toml.StripComment(sc.Text()))
			}
			values, err := toml.ParseStringArray(raw)
			if err != nil {
				return Policy{}, policyError("syntax", "line %d %s: %v", startLine, key, err)
			}
			if key == "registry_signing_keys" {
				registrySigningKeySpecs = values
			} else {
				revokedRegistryKeyIDs = values
			}
			usedRegistrySignatureFields = true
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
	if p.SchemaVersion < 1 || p.SchemaVersion > MaxSchemaVersion {
		return Policy{}, policyError("version", "unsupported policy_version %d (supported: 1-%d)", p.SchemaVersion, MaxSchemaVersion)
	}
	if usedRegistrySignatureFields && p.SchemaVersion < 2 {
		return Policy{}, policyError("version", "registry signature controls require policy_version 2")
	}
	if !p.RequireLock && len(p.AllowedRepositories) == 0 && !p.RequireRegistrySignature {
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
	keys, revoked, err := parseRegistrySigningKeys(registrySigningKeySpecs, revokedRegistryKeyIDs)
	if err != nil {
		return Policy{}, err
	}
	p.registrySigningKeys = keys
	p.revokedRegistryKeyIDs = revoked
	if p.RequireRegistrySignature {
		active := 0
		for id, key := range keys {
			if !revoked[id] && !now.Before(key.NotBefore) && now.Before(key.ExpiresAt) {
				active++
			}
		}
		if active == 0 {
			return Policy{}, policyError("syntax", "require_registry_signature needs at least one currently active, non-revoked registry signing key")
		}
	}
	sum := sha256.Sum256(b)
	p.Fingerprint = hex.EncodeToString(sum[:])
	return p, nil
}

func parseRegistrySigningKeys(specs, revokedIDs []string) (map[string]registrySigningKey, map[string]bool, error) {
	keys := make(map[string]registrySigningKey, len(specs))
	publicKeys := map[string]string{}
	for _, spec := range specs {
		parts := strings.Split(spec, "|")
		if len(parts) != 4 {
			return nil, nil, policyError("syntax", "registry_signing_keys entry must be KEY_ID|PUBLIC_KEY_BASE64|NOT_BEFORE|EXPIRES_AT")
		}
		id := parts[0]
		if !validRegistryKeyID(id) {
			return nil, nil, policyError("syntax", "invalid registry signing key id %q", id)
		}
		if _, duplicate := keys[id]; duplicate {
			return nil, nil, policyError("syntax", "duplicate registry signing key id %q", id)
		}
		decoded, err := base64.StdEncoding.DecodeString(parts[1])
		if err != nil || base64.StdEncoding.EncodeToString(decoded) != parts[1] || len(decoded) != ed25519.PublicKeySize {
			return nil, nil, policyError("syntax", "registry signing key %q has a noncanonical or invalid Ed25519 public key", id)
		}
		encodedKey := base64.StdEncoding.EncodeToString(decoded)
		if otherID, duplicate := publicKeys[encodedKey]; duplicate {
			return nil, nil, policyError("syntax", "registry signing keys %q and %q use the same public key", otherID, id)
		}
		notBefore, err := parsePolicyTimestamp(parts[2])
		if err != nil {
			return nil, nil, policyError("syntax", "registry signing key %q not-before: %v", id, err)
		}
		expiresAt, err := parsePolicyTimestamp(parts[3])
		if err != nil {
			return nil, nil, policyError("syntax", "registry signing key %q expiry: %v", id, err)
		}
		if !notBefore.Before(expiresAt) {
			return nil, nil, policyError("syntax", "registry signing key %q expiry must be after not-before", id)
		}
		publicKeys[encodedKey] = id
		keys[id] = registrySigningKey{
			PublicKey: append(ed25519.PublicKey(nil), decoded...),
			NotBefore: notBefore,
			ExpiresAt: expiresAt,
		}
	}

	revoked := make(map[string]bool, len(revokedIDs))
	for _, id := range revokedIDs {
		if !validRegistryKeyID(id) {
			return nil, nil, policyError("syntax", "invalid revoked registry key id %q", id)
		}
		if revoked[id] {
			return nil, nil, policyError("syntax", "duplicate revoked registry key id %q", id)
		}
		revoked[id] = true
	}
	return keys, revoked, nil
}

func validRegistryKeyID(id string) bool {
	if len(id) == 0 || len(id) > 64 || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for _, r := range id[1:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func parsePolicyTimestamp(raw string) (time.Time, error) {
	if !strings.HasSuffix(raw, "Z") {
		return time.Time{}, errors.New("timestamp must be canonical UTC RFC3339 ending in Z")
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil || parsed.UTC().Format(time.RFC3339) != raw {
		return time.Time{}, fmt.Errorf("timestamp must be canonical UTC RFC3339: %q", raw)
	}
	return parsed, nil
}

func arrayValueComplete(raw string) bool {
	inQuote := false
	escaped := false
	for _, r := range raw {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && inQuote {
			escaped = true
			continue
		}
		if r == '"' {
			inQuote = !inQuote
			continue
		}
		if r == ']' && !inQuote {
			return true
		}
	}
	return false
}

// AuthenticateRegistry verifies the detached signature for the exact registry
// bytes before the registry package parses or acts on them. A nil byte slice
// means the registry file is absent; signed mode never substitutes a built-in
// registry or restores an unauthenticated backup in that case.
func (p Policy) AuthenticateRegistry(path string, exactBytes []byte) error {
	if !p.RequireRegistrySignature {
		return nil
	}
	_, err := p.LoadRegistrySignature(path, exactBytes)
	return err
}

// LoadRegistrySignature reads a bounded detached envelope for backup and, when
// signed mode is active, verifies it against exactBytes before returning it.
// An optional envelope is preserved in unmanaged backups without granting it
// any trust.
func (p Policy) LoadRegistrySignature(path string, exactBytes []byte) ([]byte, error) {
	if exactBytes == nil {
		if p.RequireRegistrySignature {
			return nil, policyError("registry_signature_missing", "signed registry %s is missing; provision the exact registry and %s together", path, path+".sig")
		}
		return nil, nil
	}
	signaturePath := path + ".sig"
	info, err := os.Lstat(signaturePath)
	if errors.Is(err, os.ErrNotExist) {
		if p.RequireRegistrySignature {
			return nil, policyError("registry_signature_missing", "detached registry signature %s is missing", signaturePath)
		}
		return nil, nil
	}
	if err != nil {
		return nil, policyError("registry_signature_invalid", "inspect detached registry signature %s: %v", signaturePath, err)
	}
	if !info.Mode().IsRegular() {
		return nil, policyError("registry_signature_invalid", "detached registry signature %s must be a regular non-symlink file", signaturePath)
	}
	if info.Size() <= 0 || info.Size() > maxRegistrySignatureFileSize {
		return nil, policyError("registry_signature_invalid", "detached registry signature %s has invalid size %d (maximum %d)", signaturePath, info.Size(), maxRegistrySignatureFileSize)
	}
	f, err := os.Open(signaturePath)
	if err != nil {
		return nil, policyError("registry_signature_invalid", "open detached registry signature %s: %v", signaturePath, err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, maxRegistrySignatureFileSize+1))
	closeErr := f.Close()
	if readErr != nil {
		return nil, policyError("registry_signature_invalid", "read detached registry signature %s: %v", signaturePath, readErr)
	}
	if closeErr != nil {
		return nil, policyError("registry_signature_invalid", "close detached registry signature %s: %v", signaturePath, closeErr)
	}
	if int64(len(raw)) != info.Size() || len(raw) > maxRegistrySignatureFileSize {
		return nil, policyError("registry_signature_invalid", "detached registry signature %s changed while being read", signaturePath)
	}
	if err := p.AuthenticateRegistrySnapshot(path, exactBytes, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// AuthenticateRegistrySnapshot verifies registry bytes and an already-read
// detached envelope, for example while validating a backup before parsing it.
func (p Policy) AuthenticateRegistrySnapshot(path string, exactBytes, envelope []byte) error {
	if !p.RequireRegistrySignature {
		return nil
	}
	if exactBytes == nil {
		return policyError("registry_signature_missing", "signed registry %s is missing", path)
	}
	if envelope == nil {
		return policyError("registry_signature_missing", "detached registry signature for %s is missing", path)
	}
	return p.authenticateRegistryEnvelope(path, exactBytes, envelope, time.Now())
}

func (p Policy) authenticateRegistryEnvelope(path string, exactBytes, envelope []byte, now time.Time) error {
	parsed, err := parseRegistrySignatureEnvelope(envelope)
	if err != nil {
		return policyError("registry_signature_invalid", "%s.sig: %v", path, err)
	}
	key, trusted := p.registrySigningKeys[parsed.KeyID]
	if !trusted || p.revokedRegistryKeyIDs[parsed.KeyID] {
		return policyError("registry_signer_unauthorized", "registry signature key id %q is not trusted by the active machine policy", parsed.KeyID)
	}
	if now.Before(key.NotBefore) || !now.Before(key.ExpiresAt) {
		return policyError("registry_signer_inactive", "registry signature key id %q is valid from %s until %s", parsed.KeyID, key.NotBefore.Format(time.RFC3339), key.ExpiresAt.Format(time.RFC3339))
	}
	if !ed25519.Verify(key.PublicKey, exactBytes, parsed.Signature) {
		return policyError("registry_signature_invalid", "registry signature from key id %q does not match the exact bytes of %s", parsed.KeyID, path)
	}
	return nil
}

func (p Policy) AuthorizeRegistryMutation(operation string) error {
	if !p.RequireRegistrySignature {
		return nil
	}
	return policyError("registry_signed_readonly", "%s cannot rewrite an administrator-signed registry; provision updated registry bytes and a matching detached signature together", operation)
}

type registrySignatureEnvelope struct {
	KeyID     string
	Signature []byte
}

func parseRegistrySignatureEnvelope(raw []byte) (registrySignatureEnvelope, error) {
	var envelope registrySignatureEnvelope
	seen := map[string]bool{}
	version := 0
	algorithm := ""
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(toml.StripComment(sc.Text()))
		if line == "" {
			continue
		}
		if section, ok, err := toml.ParseSectionHeader(line); err != nil {
			return registrySignatureEnvelope{}, fmt.Errorf("line %d: %v", lineNo, err)
		} else if ok {
			return registrySignatureEnvelope{}, fmt.Errorf("line %d: unsupported section %q", lineNo, section)
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			return registrySignatureEnvelope{}, fmt.Errorf("line %d: expected key = value", lineNo)
		}
		key, value := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		if seen[key] {
			return registrySignatureEnvelope{}, fmt.Errorf("line %d: duplicate key %q", lineNo, key)
		}
		seen[key] = true
		switch key {
		case "signature_version":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return registrySignatureEnvelope{}, fmt.Errorf("line %d: signature_version must be an integer", lineNo)
			}
			version = parsed
		case "algorithm":
			parsed, err := toml.ParseQuoted(value)
			if err != nil {
				return registrySignatureEnvelope{}, fmt.Errorf("line %d algorithm: %v", lineNo, err)
			}
			algorithm = parsed
		case "key_id":
			parsed, err := toml.ParseQuoted(value)
			if err != nil {
				return registrySignatureEnvelope{}, fmt.Errorf("line %d key_id: %v", lineNo, err)
			}
			envelope.KeyID = parsed
		case "signature":
			encoded, err := toml.ParseQuoted(value)
			if err != nil {
				return registrySignatureEnvelope{}, fmt.Errorf("line %d signature: %v", lineNo, err)
			}
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil || base64.StdEncoding.EncodeToString(decoded) != encoded || len(decoded) != ed25519.SignatureSize {
				return registrySignatureEnvelope{}, fmt.Errorf("line %d: signature must be canonical base64 encoding of a %d-byte Ed25519 signature", lineNo, ed25519.SignatureSize)
			}
			envelope.Signature = decoded
		default:
			return registrySignatureEnvelope{}, fmt.Errorf("line %d: unsupported key %q", lineNo, key)
		}
	}
	if err := sc.Err(); err != nil {
		return registrySignatureEnvelope{}, err
	}
	if version != registrySignatureVersion {
		return registrySignatureEnvelope{}, fmt.Errorf("unsupported signature_version %d (supported: %d)", version, registrySignatureVersion)
	}
	if algorithm != registrySignatureAlgorithm {
		return registrySignatureEnvelope{}, fmt.Errorf("unsupported signature algorithm %q", algorithm)
	}
	if !validRegistryKeyID(envelope.KeyID) {
		return registrySignatureEnvelope{}, fmt.Errorf("invalid key_id %q", envelope.KeyID)
	}
	if len(envelope.Signature) != ed25519.SignatureSize {
		return registrySignatureEnvelope{}, errors.New("signature is required")
	}
	return envelope, nil
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
	if !strings.Contains(parts[0], ".") && !strings.Contains(parts[0], ":") && parts[0] != "localhost" {
		parts = append([]string{"docker.io"}, parts...)
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
