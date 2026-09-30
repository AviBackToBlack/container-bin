// Package terminal classifies whether the current stdin/stdout pair should use
// Docker's interactive TTY mode. It is shared by host frontends so redirected
// and captured streams retain the same conservative behavior.
package terminal

import "os"

type fileStatter interface {
	Stat() (os.FileInfo, error)
}

// Interactive reports whether both stdin and stdout are character devices.
// Any stat failure or redirected stream returns false.
func Interactive() bool {
	return interactiveFor(os.Stdin, os.Stdout)
}

func interactiveFor(stdin, stdout fileStatter) bool {
	in, err := stdin.Stat()
	if err != nil || in.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	out, err := stdout.Stat()
	return err == nil && out.Mode()&os.ModeCharDevice != 0
}
