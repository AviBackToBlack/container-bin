package wsldocker

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	rawStreamHeaderSize     = 8
	rawStreamCopyBufferSize = 32 * 1024
	maxSystemErrorSize      = 4096

	rawStreamStdin  = 0
	rawStreamStdout = 1
	rawStreamStderr = 2
	rawStreamSystem = 3
)

// CopyMultiplexedOutput decodes Docker's non-TTY raw-stream framing and copies
// stdout and stderr payloads to their matching writers. A clean EOF is accepted
// only between complete frames. The returned count is the number of payload
// bytes written before completion or failure.
func CopyMultiplexedOutput(stdout, stderr io.Writer, source io.Reader) (int64, error) {
	if stdout == nil {
		return 0, errors.New("Docker raw-stream stdout writer is required")
	}
	if stderr == nil {
		return 0, errors.New("Docker raw-stream stderr writer is required")
	}
	if source == nil {
		return 0, errors.New("Docker raw-stream source is required")
	}

	var (
		header [rawStreamHeaderSize]byte
		buffer [rawStreamCopyBufferSize]byte
		total  int64
	)
	for {
		n, err := io.ReadFull(source, header[:])
		if err == io.EOF && n == 0 {
			return total, nil
		}
		if err != nil {
			return total, fmt.Errorf("read Docker raw-stream frame header: %w", err)
		}
		if header[1] != 0 || header[2] != 0 || header[3] != 0 {
			return total, errors.New("Docker raw-stream frame has non-zero reserved header bytes")
		}

		payloadSize := int64(binary.BigEndian.Uint32(header[4:]))
		switch header[0] {
		case rawStreamStdout:
			written, err := copyRawStreamPayload(stdout, source, payloadSize, buffer[:])
			total += written
			if err != nil {
				return total, fmt.Errorf("copy Docker raw-stream stdout frame: %w", err)
			}
		case rawStreamStderr:
			written, err := copyRawStreamPayload(stderr, source, payloadSize, buffer[:])
			total += written
			if err != nil {
				return total, fmt.Errorf("copy Docker raw-stream stderr frame: %w", err)
			}
		case rawStreamSystem:
			return total, readRawStreamSystemError(source, payloadSize)
		case rawStreamStdin:
			return total, errors.New("Docker raw-stream frame unexpectedly targets stdin")
		default:
			return total, fmt.Errorf("Docker raw-stream frame uses unsupported stream type %d", header[0])
		}
	}
}

type writerOnly struct {
	io.Writer
}

func copyRawStreamPayload(destination io.Writer, source io.Reader, size int64, buffer []byte) (int64, error) {
	if size == 0 {
		return 0, nil
	}
	written, err := io.CopyBuffer(writerOnly{destination}, io.LimitReader(source, size), buffer)
	if err != nil {
		return written, err
	}
	if written != size {
		return written, io.ErrUnexpectedEOF
	}
	return written, nil
}

func readRawStreamSystemError(source io.Reader, size int64) error {
	if size > maxSystemErrorSize {
		return fmt.Errorf("Docker raw-stream system error exceeds %d bytes", maxSystemErrorSize)
	}
	payload := make([]byte, int(size))
	if _, err := io.ReadFull(source, payload); err != nil {
		return fmt.Errorf("read Docker raw-stream system error: %w", err)
	}
	message := string(payload)
	if message == "" || !utf8.Valid(payload) || strings.TrimSpace(message) != message {
		return errors.New("Docker raw-stream reported an invalid system error")
	}
	for _, r := range message {
		if unicode.IsControl(r) {
			return errors.New("Docker raw-stream reported an invalid system error")
		}
	}
	return fmt.Errorf("Docker raw-stream system error: %q", message)
}
