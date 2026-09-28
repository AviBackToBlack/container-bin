//go:build image_trust_e2e

package policy

import "time"

// LoadQualificationFile loads an explicitly selected policy for the opt-in
// image-trust E2E test. The build tag keeps this ownership-bypass helper out of
// production binaries; parsing, expiry checks and every file pin remain strict.
func LoadQualificationFile(path string) (Policy, error) {
	return loadAt(path, func(string) error { return nil }, time.Now())
}
