// Package wslvolume defines deterministic Docker volume identity for one
// native WSL2 distribution, machine and user namespace and binds exact
// inspect/create/remove/discovery operations to the proven Docker Desktop WSL
// control transport. It can preflight and ensure a stateful profile's complete
// binding set; container creation and state commands compose this boundary.
package wslvolume

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
	"github.com/AviBackToBlack/container-bin/internal/registry"
)

const (
	NamespaceLabel      = "cb.wsl_namespace"
	projectDomain       = "container-bin/wsl2-project/v1\x00"
	maxVolumeNameLength = 255
)

// Volume is an immutable deterministic name plus its complete ownership labels.
type Volume struct {
	name   string
	labels map[string]string
}

func (v Volume) Name() string { return v.name }

// Labels returns a copy of the complete label set required at creation time.
func (v Volume) Labels() map[string]string { return cloneLabels(v.labels) }

// Matches requires the exact constructed name and complete label set. A broad
// namespace prefix/label match is suitable for discovery, never adoption.
func (v Volume) Matches(name string, labels map[string]string) bool {
	if name != v.name || len(labels) != len(v.labels) {
		return false
	}
	for key, value := range v.labels {
		if labels[key] != value {
			return false
		}
	}
	return true
}

// Scope is the opaque distribution/machine/user namespace used by every WSL
// volume operation.
type Scope struct {
	namespace string
	prefix    string
}

// New accepts only the versioned opaque namespace produced by hostenv.
func New(layout hostenv.WSLLayout) (Scope, error) {
	if !validNamespace(layout.StateNamespace) {
		return Scope{}, fmt.Errorf("invalid native WSL state namespace %q", layout.StateNamespace)
	}
	return Scope{
		namespace: layout.StateNamespace,
		prefix:    "cb-" + layout.StateNamespace + "-",
	}, nil
}

func (s Scope) Namespace() string { return s.namespace }

func (s Scope) Prefix() string { return s.prefix }

// FilterLabel is the exact Docker label filter for this WSL namespace.
func (s Scope) FilterLabel() string { return NamespaceLabel + "=" + s.namespace }

// Shared returns identity for state intentionally shared across projects only
// inside this exact WSL distribution/machine/user namespace.
func (s Scope) Shared(group, logical string) (Volume, error) {
	if err := validateOwner(group, logical); err != nil {
		return Volume{}, err
	}
	name, err := s.volumeName(group, logical, "")
	if err != nil {
		return Volume{}, err
	}
	return Volume{
		name: name,
		labels: map[string]string{
			"cb.managed":   "true",
			"cb.kind":      "shared",
			"cb.owner":     group + "/" + logical,
			NamespaceLabel: s.namespace,
		},
	}, nil
}

// Project returns identity for state scoped to one canonical Linux project
// path. Hashing is case-sensitive; native Linux paths must never inherit the
// Windows case-folding rule.
func (s Scope) Project(group, logical, root string) (Volume, error) {
	if err := validateOwner(group, logical); err != nil {
		return Volume{}, err
	}
	hash, err := ProjectHash(root)
	if err != nil {
		return Volume{}, err
	}
	name, err := s.volumeName(group, logical, "-"+hash)
	if err != nil {
		return Volume{}, err
	}
	return Volume{
		name: name,
		labels: map[string]string{
			"cb.managed":      "true",
			"cb.kind":         "project",
			"cb.owner":        group + "/" + logical,
			"cb.project_path": root,
			"cb.project_hash": hash,
			NamespaceLabel:    s.namespace,
		},
	}, nil
}

func (s Scope) volumeName(group, logical, suffix string) (string, error) {
	name := s.prefix + ownerName(group, logical) + suffix
	if len(name) > maxVolumeNameLength {
		return "", fmt.Errorf("WSL volume name for owner %q exceeds %d bytes", group+"/"+logical, maxVolumeNameLength)
	}
	return name, nil
}

func ownerName(group, logical string) string {
	return strconv.Itoa(len(group)) + "-" + group + "-" + strconv.Itoa(len(logical)) + "-" + logical
}

func cloneLabels(labels map[string]string) map[string]string {
	clone := make(map[string]string, len(labels))
	for key, value := range labels {
		clone[key] = value
	}
	return clone
}

// ProjectHash hashes one already-canonical absolute Linux path without case
// folding or Unicode normalization.
func ProjectHash(root string) (string, error) {
	if err := validateProjectRoot(root); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(projectDomain + root))
	return hex.EncodeToString(sum[:6]), nil
}

func validateOwner(group, logical string) error {
	if !registry.ValidToolName(group) {
		return fmt.Errorf("invalid WSL volume state group %q", group)
	}
	if !registry.ValidToolName(logical) {
		return fmt.Errorf("invalid WSL logical volume name %q", logical)
	}
	return nil
}

func validateProjectRoot(root string) error {
	if root == "" || !utf8.ValidString(root) || !path.IsAbs(root) || path.Clean(root) != root || root == "/" || strings.ContainsRune(root, '\\') {
		return errors.New("WSL project root must be a canonical absolute non-root Linux path")
	}
	for _, r := range root {
		if unicode.IsControl(r) {
			return errors.New("WSL project root contains a control character")
		}
	}
	return nil
}

func validNamespace(namespace string) bool {
	const prefix = "wsl2-"
	if len(namespace) != len(prefix)+32 || !strings.HasPrefix(namespace, prefix) {
		return false
	}
	for _, r := range namespace[len(prefix):] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
