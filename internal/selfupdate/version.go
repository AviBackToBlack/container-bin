package selfupdate

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type semanticVersion struct {
	raw        string
	major      uint64
	minor      uint64
	patch      uint64
	prerelease []string
}

func parseVersion(raw string) (semanticVersion, error) {
	if !strings.HasPrefix(raw, "v") || strings.ContainsAny(raw, "+/\\ 	\r\n") {
		return semanticVersion{}, fmt.Errorf("version %q must be canonical vMAJOR.MINOR.PATCH with an optional prerelease", raw)
	}
	coreAndPre := strings.SplitN(raw[1:], "-", 2)
	if len(coreAndPre) > 2 {
		return semanticVersion{}, fmt.Errorf("version %q has an invalid prerelease", raw)
	}
	core := strings.Split(coreAndPre[0], ".")
	if len(core) != 3 {
		return semanticVersion{}, fmt.Errorf("version %q must have three numeric components", raw)
	}
	parts := make([]uint64, 3)
	for i, value := range core {
		if value == "" || (len(value) > 1 && value[0] == '0') {
			return semanticVersion{}, fmt.Errorf("version %q has a non-canonical numeric component", raw)
		}
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return semanticVersion{}, fmt.Errorf("version %q has an invalid numeric component", raw)
		}
		parts[i] = parsed
	}
	v := semanticVersion{raw: raw, major: parts[0], minor: parts[1], patch: parts[2]}
	if len(coreAndPre) == 2 {
		if coreAndPre[1] == "" {
			return semanticVersion{}, fmt.Errorf("version %q has an empty prerelease", raw)
		}
		for _, identifier := range strings.Split(coreAndPre[1], ".") {
			if identifier == "" {
				return semanticVersion{}, fmt.Errorf("version %q has an empty prerelease identifier", raw)
			}
			numeric := true
			for _, r := range identifier {
				if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '-' {
					if r < '0' || r > '9' {
						numeric = false
					}
					continue
				}
				return semanticVersion{}, fmt.Errorf("version %q has an invalid prerelease identifier", raw)
			}
			if numeric && len(identifier) > 1 && identifier[0] == '0' {
				return semanticVersion{}, fmt.Errorf("version %q has a non-canonical numeric prerelease identifier", raw)
			}
			v.prerelease = append(v.prerelease, identifier)
		}
	}
	return v, nil
}

func (v semanticVersion) compare(other semanticVersion) int {
	for _, pair := range [][2]uint64{{v.major, other.major}, {v.minor, other.minor}, {v.patch, other.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if len(v.prerelease) == 0 && len(other.prerelease) == 0 {
		return 0
	}
	if len(v.prerelease) == 0 {
		return 1
	}
	if len(other.prerelease) == 0 {
		return -1
	}
	for i := 0; i < len(v.prerelease) && i < len(other.prerelease); i++ {
		a, b := v.prerelease[i], other.prerelease[i]
		if a == b {
			continue
		}
		aNumber := numericIdentifier(a)
		bNumber := numericIdentifier(b)
		switch {
		case aNumber && bNumber:
			if len(a) < len(b) || (len(a) == len(b) && a < b) {
				return -1
			}
			return 1
		case aNumber:
			return -1
		case bNumber:
			return 1
		case a < b:
			return -1
		default:
			return 1
		}
	}
	if len(v.prerelease) < len(other.prerelease) {
		return -1
	}
	if len(v.prerelease) > len(other.prerelease) {
		return 1
	}
	return 0
}

func numericIdentifier(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != ""
}

func (v semanticVersion) isDevelopment() bool {
	if v.raw == "dev" {
		return true
	}
	for _, identifier := range v.prerelease {
		if strings.EqualFold(identifier, "dev") {
			return true
		}
	}
	return false
}

func currentVersion(raw string) (semanticVersion, error) {
	if raw == "dev" {
		return semanticVersion{}, errors.New("self-update is unavailable for development builds")
	}
	v, err := parseVersion(raw)
	if err != nil {
		return semanticVersion{}, fmt.Errorf("current build version is not release-qualified: %w", err)
	}
	if v.isDevelopment() {
		return semanticVersion{}, errors.New("self-update is unavailable for development builds")
	}
	return v, nil
}
