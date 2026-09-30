package terminal

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestInteractiveRequiresCharacterDeviceStreams(t *testing.T) {
	character := testStatter{info: testFileInfo{mode: os.ModeCharDevice}}
	regular := testStatter{info: testFileInfo{mode: 0o644}}
	broken := testStatter{err: errors.New("stat failed")}

	for _, test := range []struct {
		name        string
		stdin       testStatter
		stdout      testStatter
		interactive bool
	}{
		{name: "both character devices", stdin: character, stdout: character, interactive: true},
		{name: "stdin pipe", stdin: regular, stdout: character},
		{name: "stdout redirected", stdin: character, stdout: regular},
		{name: "stdin stat error", stdin: broken, stdout: character},
		{name: "stdout stat error", stdin: character, stdout: broken},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := interactiveFor(test.stdin, test.stdout); got != test.interactive {
				t.Fatalf("interactiveFor() = %v, want %v", got, test.interactive)
			}
		})
	}
}

type testStatter struct {
	info os.FileInfo
	err  error
}

func (s testStatter) Stat() (os.FileInfo, error) { return s.info, s.err }

type testFileInfo struct {
	mode os.FileMode
}

func (f testFileInfo) Name() string       { return "test" }
func (f testFileInfo) Size() int64        { return 0 }
func (f testFileInfo) Mode() os.FileMode  { return f.mode }
func (f testFileInfo) ModTime() time.Time { return time.Time{} }
func (f testFileInfo) IsDir() bool        { return false }
func (f testFileInfo) Sys() any           { return nil }
