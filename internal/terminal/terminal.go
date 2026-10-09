// Package terminal classifies whether the current stdin/stdout pair should use
// Docker's interactive TTY mode. It is shared by host frontends so redirected
// and captured streams retain the same conservative behavior.
package terminal

import "os"

type fileStatter interface {
	Stat() (os.FileInfo, error)
}

// Interactive reports whether stdin and stdout can use interactive TTY mode.
// Windows additionally requires real console handles: NUL is a character
// device but must remain redirected, non-TTY output.
func Interactive() bool {
	return interactiveFor(os.Stdin, os.Stdout) && platformTerminalPair(os.Stdin, os.Stdout)
}

func interactiveFor(stdin, stdout fileStatter) bool {
	in, err := stdin.Stat()
	if err != nil || in.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	out, err := stdout.Stat()
	return err == nil && out.Mode()&os.ModeCharDevice != 0
}
