// Package wslvolume defines deterministic Docker volume identity for one
// native WSL2 distribution, machine and user namespace. It does not contact
// Docker; creation and lifecycle wiring remain separate operations.
package wslvolume

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
	"github.com/AviBackToBlack/container-bin/internal/registry"
)

const (
	NamespaceLabel = "cb.wsl_namespace"
	projectDomain  = "container-bin/wsl2-project/v1\x00"
)

// Volume is a deterministic name plus the complete ownership labels required
// when the volume is created.
type Volume struct {
	Name   string
	Labels map[string]string
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

// Owns requires both the namespace name prefix and exact ownership labels.
// Either signal alone is insufficient for mutation or adoption.
func (s Scope) Owns(name string, labels map[string]string) bool {
	return len(name) > len(s.prefix) && strings.HasPrefix(name, s.prefix) &&
		labels["cb.managed"] == "true" &&
		labels[NamespaceLabel] == s.namespace
}

// Shared returns identity for state intentionally shared across projects only
// inside this exact WSL distribution/machine/user namespace.
func (s Scope) Shared(group, logical string) (Volume, error) {
	if err := validateOwner(group, logical); err != nil {
		return Volume{}, err
	}
	return Volume{
		Name: s.prefix + group + "-" + logical,
		Labels: map[string]string{
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
	return Volume{
		Name: s.prefix + group + "-" + logical + "-" + hash,
		Labels: map[string]string{
			"cb.managed":      "true",
			"cb.kind":         "project",
			"cb.owner":        group + "/" + logical,
			"cb.project_path": root,
			"cb.project_hash": hash,
			NamespaceLabel:    s.namespace,
		},
	}, nil
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
