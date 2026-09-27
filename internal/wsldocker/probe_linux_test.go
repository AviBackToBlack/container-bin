//go:build linux

package wsldocker

import "testing"

func TestCappedBufferBoundsRetainedOutput(t *testing.T) {
	buffer := cappedBuffer{max: 4}
	written, err := buffer.Write([]byte("abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if written != 6 || buffer.String() != "abcd" || !buffer.exceeded {
		t.Fatalf("Write() = (%d, %q, exceeded=%t)", written, buffer.String(), buffer.exceeded)
	}
	written, err = buffer.Write([]byte("gh"))
	if err != nil {
		t.Fatal(err)
	}
	if written != 2 || buffer.String() != "abcd" || !buffer.exceeded {
		t.Fatalf("second Write() = (%d, %q, exceeded=%t)", written, buffer.String(), buffer.exceeded)
	}
}
