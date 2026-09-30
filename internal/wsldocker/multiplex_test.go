package wsldocker

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestCopyMultiplexedOutputRoutesCompleteFrames(t *testing.T) {
	large := bytes.Repeat([]byte("x"), rawStreamCopyBufferSize+17)
	stream := bytes.Join([][]byte{
		rawStreamFrame(rawStreamStdout, []byte("out-1")),
		rawStreamFrame(rawStreamStderr, []byte("err")),
		rawStreamFrame(rawStreamStdout, nil),
		rawStreamFrame(rawStreamStdout, large),
	}, nil)

	var stdout, stderr bytes.Buffer
	written, err := CopyMultiplexedOutput(&stdout, &stderr, &chunkedReader{source: bytes.NewReader(stream), limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	wantStdout := append([]byte("out-1"), large...)
	if !bytes.Equal(stdout.Bytes(), wantStdout) {
		t.Fatalf("stdout length = %d, want %d", stdout.Len(), len(wantStdout))
	}
	if stderr.String() != "err" {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if written != int64(len(wantStdout)+stderr.Len()) {
		t.Fatalf("written = %d", written)
	}
}

func TestCopyMultiplexedOutputRejectsMalformedFrames(t *testing.T) {
	tests := []struct {
		name      string
		stream    []byte
		wantError string
		wantEOF   bool
		wantOut   string
		wantCount int64
	}{
		{name: "truncated header", stream: []byte{rawStreamStdout, 0, 0}, wantEOF: true},
		{name: "reserved bytes", stream: []byte{rawStreamStdout, 0, 1, 0, 0, 0, 0, 0}, wantError: "reserved"},
		{name: "stdin", stream: rawStreamFrame(rawStreamStdin, nil), wantError: "stdin"},
		{name: "unknown", stream: rawStreamFrame(7, nil), wantError: "stream type 7"},
		{name: "truncated payload", stream: append(rawStreamHeader(rawStreamStdout, 5), []byte("abc")...), wantEOF: true, wantOut: "abc", wantCount: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			written, err := CopyMultiplexedOutput(&stdout, &stderr, bytes.NewReader(test.stream))
			if err == nil {
				t.Fatal("CopyMultiplexedOutput() accepted malformed frame")
			}
			if test.wantEOF && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("error = %v, want io.ErrUnexpectedEOF", err)
			}
			if test.wantError != "" && !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want substring %q", err, test.wantError)
			}
			if stdout.String() != test.wantOut || stderr.Len() != 0 || written != test.wantCount {
				t.Fatalf("stdout=%q stderr=%q written=%d", stdout.String(), stderr.String(), written)
			}
		})
	}
}

func TestCopyMultiplexedOutputPropagatesWriterFailure(t *testing.T) {
	stream := rawStreamFrame(rawStreamStdout, []byte("abcd"))
	written, err := CopyMultiplexedOutput(shortWriter{}, io.Discard, bytes.NewReader(stream))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("error = %v, want io.ErrShortWrite", err)
	}
	if written != 3 {
		t.Fatalf("written = %d, want 3", written)
	}
}

func TestCopyMultiplexedOutputHandlesSystemErrors(t *testing.T) {
	t.Run("safe", func(t *testing.T) {
		written, err := CopyMultiplexedOutput(io.Discard, io.Discard, bytes.NewReader(rawStreamFrame(rawStreamSystem, []byte("daemon failed"))))
		if written != 0 || err == nil || !strings.Contains(err.Error(), `"daemon failed"`) {
			t.Fatalf("written=%d error=%v", written, err)
		}
	})

	t.Run("unsafe", func(t *testing.T) {
		written, err := CopyMultiplexedOutput(io.Discard, io.Discard, bytes.NewReader(rawStreamFrame(rawStreamSystem, []byte("bad\nforged"))))
		if written != 0 || err == nil || !strings.Contains(err.Error(), "invalid system error") {
			t.Fatalf("written=%d error=%v", written, err)
		}
		if strings.Contains(err.Error(), "forged") {
			t.Fatalf("unsafe payload leaked in error: %v", err)
		}
	})

	t.Run("invalid UTF-8", func(t *testing.T) {
		_, err := CopyMultiplexedOutput(io.Discard, io.Discard, bytes.NewReader(rawStreamFrame(rawStreamSystem, []byte{0xff})))
		if err == nil || !strings.Contains(err.Error(), "invalid system error") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		stream := rawStreamHeader(rawStreamSystem, maxSystemErrorSize+1)
		_, err := CopyMultiplexedOutput(io.Discard, io.Discard, bytes.NewReader(stream))
		if err == nil || !strings.Contains(err.Error(), "exceeds 4096 bytes") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("truncated", func(t *testing.T) {
		stream := append(rawStreamHeader(rawStreamSystem, 5), []byte("abc")...)
		_, err := CopyMultiplexedOutput(io.Discard, io.Discard, bytes.NewReader(stream))
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("error = %v, want io.ErrUnexpectedEOF", err)
		}
	})

	t.Run("truncated before payload", func(t *testing.T) {
		_, err := CopyMultiplexedOutput(io.Discard, io.Discard, bytes.NewReader(rawStreamHeader(rawStreamSystem, 5)))
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("error = %v, want io.ErrUnexpectedEOF", err)
		}
	})
}

func TestCopyMultiplexedOutputRejectsMissingInputs(t *testing.T) {
	tests := []struct {
		name   string
		stdout io.Writer
		stderr io.Writer
		source io.Reader
	}{
		{name: "stdout", stderr: io.Discard, source: bytes.NewReader(nil)},
		{name: "stderr", stdout: io.Discard, source: bytes.NewReader(nil)},
		{name: "source", stdout: io.Discard, stderr: io.Discard},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := CopyMultiplexedOutput(test.stdout, test.stderr, test.source); err == nil {
				t.Fatal("CopyMultiplexedOutput() accepted a missing input")
			}
		})
	}
}

type chunkedReader struct {
	source io.Reader
	limit  int
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if len(p) > r.limit {
		p = p[:r.limit]
	}
	return r.source.Read(p)
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) {
	return len(p) - 1, nil
}

func rawStreamFrame(stream byte, payload []byte) []byte {
	return append(rawStreamHeader(stream, len(payload)), payload...)
}

func rawStreamHeader(stream byte, payloadSize int) []byte {
	header := make([]byte, rawStreamHeaderSize)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(payloadSize))
	return header
}
