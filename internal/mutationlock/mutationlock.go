// Package mutationlock serializes registry-mutating cb commands through an
// exclusive lock file next to the registry. It deliberately knows nothing
// about signals or process exit codes: interrupt handling and exit-code policy
// live in main, which wraps Acquire.
package mutationlock

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Wait is how long Acquire keeps retrying before reporting contention.
const (
	Wait          = 5 * time.Second
	retryInterval = 50 * time.Millisecond
)

func PathFor(cfgPath string) string {
	return filepath.Join(filepath.Dir(cfgPath), "container-bin.mutation.lock")
}

func readHolder(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "unknown holder"
	}
	line := strings.TrimSpace(string(data))
	if line == "" {
		return "unknown holder"
	}
	return line
}

func Acquire(cfgPath string, wait time.Duration) (func(), error) {
	path := PathFor(cfgPath)
	start := time.Now()
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err == nil {
			token := make([]byte, 16)
			if _, rerr := rand.Read(token); rerr != nil {
				_ = f.Close()
				_ = os.Remove(path)
				return nil, rerr
			}
			tokenHex := hex.EncodeToString(token)
			line := fmt.Sprintf("pid=%d token=%s at %s", os.Getpid(), tokenHex, time.Now().Format(time.RFC3339))
			if _, werr := f.WriteString(line); werr != nil {
				_ = f.Close()
				_ = os.Remove(path)
				return nil, werr
			}
			if cerr := f.Close(); cerr != nil {
				_ = os.Remove(path)
				return nil, cerr
			}
			var once sync.Once
			return func() {
				once.Do(func() {
					data, err := os.ReadFile(path)
					if err != nil {
						return
					}
					if strings.TrimSpace(string(data)) != line {
						return
					}
					_ = os.Remove(path)
				})
			}, nil
		}
		// Windows keeps a deleted file in a pending-delete state until every
		// handle to it closes, and reports ERROR_ACCESS_DENIED rather than
		// ERROR_FILE_EXISTS for a create during that window. A release racing
		// an acquire therefore looks like a permission failure, so treat it as
		// contention and let the timeout decide.
		if !os.IsExist(err) && !(runtime.GOOS == "windows" && os.IsPermission(err)) {
			return nil, err
		}
		if time.Since(start) >= wait {
			// A permission error with no lock file to blame is a genuinely
			// unwritable directory, not contention; report what actually failed.
			if os.IsPermission(err) {
				if _, statErr := os.Stat(path); statErr != nil {
					return nil, fmt.Errorf("create %s: %w", path, err)
				}
			}
			return nil, fmt.Errorf("another cb command is mutating the registry (holder: %s); retry, or delete %s if no cb process is running", readHolder(path), path)
		}
		time.Sleep(retryInterval)
	}
}
