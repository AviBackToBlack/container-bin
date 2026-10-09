//go:build !windows

package terminal

import "os"

func platformTerminalPair(_, _ *os.File) bool { return true }
