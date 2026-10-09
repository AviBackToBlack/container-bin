//go:build windows

package terminal

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

func TestConsolePairRequiresBothHandles(t *testing.T) {
	for _, fail := range []syscall.Handle{0, 10, 20} {
		query := func(handle syscall.Handle, _ *uint32) error {
			if handle == fail {
				return errors.New("not a console")
			}
			return nil
		}
		if got := consolePair(10, 20, query); got != (fail == 0) {
			t.Fatalf("consolePair with rejected handle %d = %v", fail, got)
		}
	}
}

func TestInteractiveRejectsNULCharacterDevice(t *testing.T) {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if !interactiveFor(null, null) {
		t.Fatal("NUL must reproduce the character-device false positive")
	}
	originalStdin, originalStdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = null, null
	defer func() { os.Stdin, os.Stdout = originalStdin, originalStdout }()
	if Interactive() {
		t.Fatal("NUL character devices enabled TTY mode")
	}
}
