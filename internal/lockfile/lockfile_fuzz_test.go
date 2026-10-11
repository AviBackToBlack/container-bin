package lockfile

import (
	"bytes"
	"reflect"
	"testing"
)

func FuzzParseLockFile(f *testing.F) {
	for _, seed := range [][]byte{
		{},
		[]byte("lock_version = 1\n"),
		[]byte("lock_version = 2\nlock_version = 1\n"),
		[]byte("lock_version = 999\n"),
		render(validTrustLock(2)),
	} {
		f.Add(seed)
	}
	// Cover legacy repository and local-image entries without trust evidence.
	legacy := validTrustLock(1)
	entry := legacy.Images["node:24-slim"]
	entry.Trust = nil
	legacy.Images[entry.Configured] = entry
	f.Add(render(legacy))
	entry.Configured, entry.Resolved = "local/tool:dev", entry.Digest
	legacy.Images = map[string]LockEntry{entry.Configured: entry}
	f.Add(render(legacy))
	f.Fuzz(func(t *testing.T, input []byte) {
		before := append([]byte(nil), input...)
		lock, err := parseLockFile(input, 2)
		if !bytes.Equal(input, before) {
			t.Fatal("lockfile parsing mutated its input")
		}
		if err != nil {
			return
		}
		canonical := render(lock)
		again, err := parseLockFile(canonical, 2)
		if err != nil || !reflect.DeepEqual(lock, again) {
			t.Fatalf("accepted lockfile failed semantic round-trip: %v", err)
		}
		if !bytes.Equal(canonical, render(again)) {
			t.Fatal("canonical lockfile rendering is unstable")
		}
		if lock.Version == 2 {
			if _, err := parseLockFile(input, 1); err == nil {
				t.Fatal("v2 lockfile accepted by v1-only parser")
			}
		}
	})
}
