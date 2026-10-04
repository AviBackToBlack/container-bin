package wslrun

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
	"github.com/AviBackToBlack/container-bin/internal/wsldocker"
	"github.com/AviBackToBlack/container-bin/internal/wslvolume"
)

type fakeAttach struct {
	mu          sync.Mutex
	reader      *bytes.Reader
	writes      bytes.Buffer
	multiplexed bool
	closedWrite bool
	closed      bool
	writeDone   chan struct{}
	writeOnce   sync.Once
	closeErr    error
}

func (s *fakeAttach) Read(p []byte) (int, error) { return s.reader.Read(p) }
func (s *fakeAttach) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes.Write(p)
}
func (s *fakeAttach) CloseWrite() error {
	s.closedWrite = true
	s.writeOnce.Do(func() {
		if s.writeDone != nil {
			close(s.writeDone)
		}
	})
	return nil
}
func (s *fakeAttach) Close() error      { s.closed = true; return s.closeErr }
func (s *fakeAttach) Multiplexed() bool { return s.multiplexed }

func TestExecuteToolStreamsMultiplexedIOAndPropagatesExitCode(t *testing.T) {
	stream := &fakeAttach{reader: bytes.NewReader(append(rawFrame(1, "out"), rawFrame(2, "err")...)), multiplexed: true, writeDone: make(chan struct{})}
	var stdout, stderr bytes.Buffer
	scope, err := wslvolume.New(hostenv.WSLLayout{StateNamespace: testNamespace})
	if err != nil {
		t.Fatal(err)
	}
	volume, err := scope.Shared("demo", "cache")
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	deps := successfulRunDependencies(t, stream, &stdout, &stderr, &calls)
	plan := toolPlan{
		spec:    wsldocker.ContainerCreateSpec{Tool: "demo", Namespace: testNamespace, Image: "demo:1", WorkingDirectory: "/root"},
		volumes: []wslvolume.Volume{volume},
	}
	code, err := executeTool(context.Background(), plan, deps)
	if err != nil {
		t.Fatal(err)
	}
	if code != 23 || stdout.String() != "out" || stderr.String() != "err" {
		t.Fatalf("result code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !stream.closedWrite || !stream.closed || stream.writes.String() != "input" {
		t.Fatalf("stream state write=%q closeWrite=%t close=%t", stream.writes.String(), stream.closedWrite, stream.closed)
	}
	wantCalls := []string{"ensure:" + volume.Name(), "create", "events", "attach", "start", "wait", "remove"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestExecuteToolAppliesTTYSizeAndForwardsHostEvents(t *testing.T) {
	stream := &fakeAttach{reader: bytes.NewReader([]byte("tty output")), writeDone: make(chan struct{})}
	var stdout, stderr bytes.Buffer
	var calls []string
	deps := successfulRunDependencies(t, stream, &stdout, &stderr, &calls)
	events := make(chan hostEvent, 2)
	events <- hostEvent{signal: 2}
	events <- hostEvent{resize: true, height: 40, width: 120}
	close(events)
	deps.prepareTerminal = func(tty bool) (terminalControl, error) {
		if !tty {
			t.Fatal("TTY plan was prepared as non-interactive")
		}
		return terminalControl{height: 24, width: 80, restore: func() error { return nil }}, nil
	}
	deps.startEvents = func(tty bool) (<-chan hostEvent, func(), error) {
		if !tty {
			t.Fatal("TTY events were started as non-interactive")
		}
		return events, func() {}, nil
	}
	var sizes [][2]uint16
	waitGate := make(chan struct{})
	deps.resize = func(_ context.Context, _ string, height, width uint16) error {
		sizes = append(sizes, [2]uint16{height, width})
		if len(sizes) == 2 {
			close(waitGate)
		}
		return nil
	}
	var signals []int
	deps.signal = func(_ context.Context, _ string, signal int) error {
		signals = append(signals, signal)
		return nil
	}
	deps.wait = func(context.Context, string) (int, error) {
		calls = append(calls, "wait")
		<-waitGate
		return 130, nil
	}
	plan := toolPlan{spec: wsldocker.ContainerCreateSpec{
		Tool: "demo", Namespace: testNamespace, Image: "demo:1", WorkingDirectory: "/root", TTY: true,
	}}
	code, err := executeTool(context.Background(), plan, deps)
	if err != nil {
		t.Fatal(err)
	}
	if code != 130 || stdout.String() != "tty output" || stderr.Len() != 0 {
		t.Fatalf("TTY result code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if got, want := sizes, [][2]uint16{{24, 80}, {40, 120}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("terminal sizes = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(signals, []int{2}) {
		t.Fatalf("forwarded signals = %#v", signals)
	}
}

func TestExecuteToolPreservesFastTTYExitAcrossInitialResizeRace(t *testing.T) {
	stream := &fakeAttach{reader: bytes.NewReader([]byte("done")), writeDone: make(chan struct{})}
	var stdout, stderr bytes.Buffer
	var calls []string
	deps := successfulRunDependencies(t, stream, &stdout, &stderr, &calls)
	deps.prepareTerminal = func(bool) (terminalControl, error) {
		return terminalControl{height: 24, width: 80, restore: func() error { return nil }}, nil
	}
	deps.resize = func(context.Context, string, uint16, uint16) error {
		return &wsldocker.APIError{StatusCode: http.StatusConflict, Message: "container is not running"}
	}
	deps.wait = func(context.Context, string) (int, error) {
		calls = append(calls, "wait")
		return 7, nil
	}
	plan := toolPlan{spec: wsldocker.ContainerCreateSpec{
		Tool: "demo", Namespace: testNamespace, Image: "demo:1", WorkingDirectory: "/root", TTY: true,
	}}
	code, err := executeTool(context.Background(), plan, deps)
	if err != nil || code != 7 || stdout.String() != "done" {
		t.Fatalf("fast TTY result code=%d stdout=%q err=%v", code, stdout.String(), err)
	}
}

func TestExecuteToolSkipsUnknownInitialTTYSize(t *testing.T) {
	stream := &fakeAttach{reader: bytes.NewReader([]byte("done")), writeDone: make(chan struct{})}
	var stdout, stderr bytes.Buffer
	var calls []string
	deps := successfulRunDependencies(t, stream, &stdout, &stderr, &calls)
	deps.prepareTerminal = func(bool) (terminalControl, error) {
		return terminalControl{restore: func() error { return nil }}, nil
	}
	deps.resize = func(context.Context, string, uint16, uint16) error {
		t.Fatal("unknown initial terminal size triggered a resize")
		return nil
	}
	plan := toolPlan{spec: wsldocker.ContainerCreateSpec{
		Tool: "demo", Namespace: testNamespace, Image: "demo:1", WorkingDirectory: "/root", TTY: true,
	}}
	code, err := executeTool(context.Background(), plan, deps)
	if err != nil || code != 23 || stdout.String() != "done" {
		t.Fatalf("unknown-size TTY result code=%d stdout=%q err=%v", code, stdout.String(), err)
	}
}

func TestExecuteToolPreservesExitAcrossQueuedControlEvents(t *testing.T) {
	stream := &fakeAttach{reader: bytes.NewReader([]byte("done")), writeDone: make(chan struct{})}
	var stdout, stderr bytes.Buffer
	var calls []string
	deps := successfulRunDependencies(t, stream, &stdout, &stderr, &calls)
	events := make(chan hostEvent, 2)
	events <- hostEvent{resize: true, height: 40, width: 120}
	events <- hostEvent{signal: 2}
	close(events)
	deps.startEvents = func(bool) (<-chan hostEvent, func(), error) { return events, func() {}, nil }
	deps.prepareTerminal = func(bool) (terminalControl, error) {
		return terminalControl{height: 24, width: 80, restore: func() error { return nil }}, nil
	}
	resizeCalls := 0
	deps.resize = func(context.Context, string, uint16, uint16) error {
		resizeCalls++
		if resizeCalls == 1 {
			return nil
		}
		return &wsldocker.APIError{StatusCode: http.StatusConflict, Message: "container is not running"}
	}
	controlsDone := make(chan struct{})
	deps.signal = func(context.Context, string, int) error {
		close(controlsDone)
		return &wsldocker.APIError{StatusCode: http.StatusNotFound, Message: "no such container"}
	}
	deps.wait = func(context.Context, string) (int, error) {
		calls = append(calls, "wait")
		<-controlsDone
		return 9, nil
	}
	plan := toolPlan{spec: wsldocker.ContainerCreateSpec{
		Tool: "demo", Namespace: testNamespace, Image: "demo:1", WorkingDirectory: "/root", TTY: true,
	}}
	code, err := executeTool(context.Background(), plan, deps)
	if err != nil || code != 9 || stdout.String() != "done" {
		t.Fatalf("queued-event result code=%d stdout=%q err=%v", code, stdout.String(), err)
	}
}

func TestExecuteToolForceStopsOwnedContainerAfterStreamFailure(t *testing.T) {
	malformed := rawFrame(1, "bad")
	malformed[1] = 1
	stream := &fakeAttach{
		reader: bytes.NewReader(malformed), multiplexed: true,
		writeDone: make(chan struct{}), closeErr: errors.New("close failed"),
	}
	var stdout, stderr bytes.Buffer
	var calls []string
	deps := successfulRunDependencies(t, stream, &stdout, &stderr, &calls)
	deps.startEvents = func(bool) (<-chan hostEvent, func(), error) {
		calls = append(calls, "events")
		events := make(chan hostEvent)
		close(events)
		return events, func() { calls = append(calls, "stop-events") }, nil
	}
	deps.wait = func(ctx context.Context, _ string) (int, error) {
		if _, cleanup := ctx.Deadline(); cleanup {
			calls = append(calls, "cleanup-wait")
			return 137, nil
		}
		<-ctx.Done()
		return 0, ctx.Err()
	}
	var signals []int
	deps.signal = func(_ context.Context, _ string, signal int) error {
		signals = append(signals, signal)
		return nil
	}
	plan := toolPlan{spec: wsldocker.ContainerCreateSpec{
		Tool: "demo", Namespace: testNamespace, Image: "demo:1", WorkingDirectory: "/root", RetainUntilCleanup: true,
	}}
	_, err := executeTool(context.Background(), plan, deps)
	if err == nil || !strings.Contains(err.Error(), "raw-stream frame") || !strings.Contains(err.Error(), "close native WSL attach stream: close failed") {
		t.Fatalf("stream failure = %v", err)
	}
	if !reflect.DeepEqual(signals, []int{9}) {
		t.Fatalf("cleanup signals = %#v", signals)
	}
	if !containsCall(calls, "cleanup-wait") || !containsCall(calls, "remove") {
		t.Fatalf("cleanup calls = %#v", calls)
	}
	if removeAt, stopAt := callIndex(calls, "remove"), callIndex(calls, "stop-events"); removeAt < 0 || stopAt <= removeAt {
		t.Fatalf("host events stopped before cleanup completed: %#v", calls)
	}
}

func TestExecuteToolCleansUpAfterAmbiguousStartFailure(t *testing.T) {
	stream := &fakeAttach{reader: bytes.NewReader(nil), writeDone: make(chan struct{})}
	var stdout, stderr bytes.Buffer
	var calls []string
	deps := successfulRunDependencies(t, stream, &stdout, &stderr, &calls)
	deps.start = func(context.Context, string) error {
		calls = append(calls, "start")
		return errors.New("connection closed")
	}
	deps.signal = func(context.Context, string, int) error {
		calls = append(calls, "signal")
		return errors.New("container is not running")
	}
	plan := toolPlan{spec: wsldocker.ContainerCreateSpec{
		Tool: "demo", Namespace: testNamespace, Image: "demo:1", WorkingDirectory: "/root", RetainUntilCleanup: true,
	}}
	_, err := executeTool(context.Background(), plan, deps)
	if err == nil || !strings.Contains(err.Error(), "start native WSL tool container: connection closed") || !strings.Contains(err.Error(), "stop native WSL tool container after failure: container is not running") {
		t.Fatalf("start failure = %v", err)
	}
	if got, want := calls, []string{"create", "events", "attach", "start", "signal", "remove"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanup calls = %#v, want %#v", got, want)
	}
}

func TestExecuteToolReportsCompletedInputFailureBeforeSuccessfulExit(t *testing.T) {
	inputDone := make(chan error, 1)
	inputDone <- errors.New("source read failed")
	_, err := finishToolResult(0, inputDone)
	if err == nil || !strings.Contains(err.Error(), "copy native WSL tool input: source read failed") {
		t.Fatalf("input failure = %v", err)
	}
}

func TestFinishToolResultDoesNotWaitForBlockedInput(t *testing.T) {
	inputDone := make(chan error)
	code, err := finishToolResult(23, inputDone)
	if err != nil || code != 23 {
		t.Fatalf("finishToolResult() = (%d, %v), want (23, nil)", code, err)
	}
}

func successfulRunDependencies(t *testing.T, stream *fakeAttach, stdout, stderr io.Writer, calls *[]string) runDependencies {
	t.Helper()
	return runDependencies{
		ensureVolume: func(_ context.Context, volume wslvolume.Volume) error {
			*calls = append(*calls, "ensure:"+volume.Name())
			return nil
		},
		create: func(context.Context, wsldocker.ContainerCreateSpec) (containerHandle, error) {
			*calls = append(*calls, "create")
			return containerHandle{id: strings.Repeat("a", 64), native: "owned"}, nil
		},
		attach: func(context.Context, wsldocker.AttachRequest) (attachStream, error) {
			*calls = append(*calls, "attach")
			return stream, nil
		},
		start: func(context.Context, string) error { *calls = append(*calls, "start"); return nil },
		wait: func(context.Context, string) (int, error) {
			*calls = append(*calls, "wait")
			<-stream.writeDone
			return 23, nil
		},
		resize: func(context.Context, string, uint16, uint16) error { return nil },
		signal: func(context.Context, string, int) error { return nil },
		remove: func(_ context.Context, handle containerHandle) error {
			*calls = append(*calls, "remove")
			if handle.native != "owned" {
				t.Fatalf("cleanup handle = %#v", handle)
			}
			return nil
		},
		prepareTerminal: func(bool) (terminalControl, error) { return terminalControl{restore: func() error { return nil }}, nil },
		startEvents: func(bool) (<-chan hostEvent, func(), error) {
			*calls = append(*calls, "events")
			events := make(chan hostEvent)
			close(events)
			return events, func() {}, nil
		},
		stdin:  strings.NewReader("input"),
		stdout: stdout,
		stderr: stderr,
	}
}

func rawFrame(stream byte, payload string) []byte {
	frame := make([]byte, 8+len(payload))
	frame[0] = stream
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(payload)))
	copy(frame[8:], payload)
	return frame
}

func containsCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}

func callIndex(calls []string, want string) int {
	for index, call := range calls {
		if call == want {
			return index
		}
	}
	return -1
}
