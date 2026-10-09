package wslrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
	"github.com/AviBackToBlack/container-bin/internal/wsldocker"
	"github.com/AviBackToBlack/container-bin/internal/wslvolume"
)

const (
	cleanupTimeout     = 30 * time.Second
	outputDrainTimeout = 5 * time.Second
)

type attachStream interface {
	io.ReadWriteCloser
	CloseWrite() error
	Multiplexed() bool
}

type containerHandle struct {
	id     string
	runID  string
	native any
}

type runGuard interface {
	Adopt(string) error
	Close(bool) error
}

type hostEvent struct {
	signal int
	resize bool
	height uint16
	width  uint16
	err    error
}

type terminalControl struct {
	height  uint16
	width   uint16
	restore func() error
}

type waitResult struct {
	code int
	err  error
}

type runDependencies struct {
	ensureVolume    func(context.Context, wslvolume.Volume) error
	beginRun        func(context.Context, hostenv.WSLLayout) (runGuard, error)
	create          func(context.Context, wsldocker.ContainerCreateSpec) (containerHandle, error)
	attach          func(context.Context, wsldocker.AttachRequest) (attachStream, error)
	start           func(context.Context, string) error
	wait            func(context.Context, string) (int, error)
	resize          func(context.Context, string, uint16, uint16) error
	signal          func(context.Context, string, int) error
	remove          func(context.Context, containerHandle) error
	initialTTYSize  func(bool) (uint16, uint16)
	prepareTerminal func(bool) (terminalControl, error)
	startEvents     func(bool) (<-chan hostEvent, func(), error)
	stdin           io.Reader
	stdout          io.Writer
	stderr          io.Writer
}

func executeTool(ctx context.Context, plan toolPlan, deps runDependencies) (code int, retErr error) {
	if ctx == nil {
		return 0, errors.New("native WSL tool execution requires a context")
	}
	if deps.ensureVolume == nil || deps.beginRun == nil || deps.create == nil || deps.attach == nil || deps.start == nil || deps.wait == nil ||
		deps.resize == nil || deps.signal == nil || deps.remove == nil || deps.initialTTYSize == nil || deps.prepareTerminal == nil || deps.startEvents == nil ||
		deps.stdin == nil || deps.stdout == nil || deps.stderr == nil {
		return 0, errors.New("native WSL tool execution dependencies are incomplete")
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	runtimeSpec := plan.spec
	runtimeSpec.TerminalHeight, runtimeSpec.TerminalWidth = deps.initialTTYSize(runtimeSpec.TTY)
	for _, volume := range plan.volumes {
		if err := deps.ensureVolume(runCtx, volume); err != nil {
			return 0, fmt.Errorf("ensure native WSL volume %s: %w", volume.Name(), err)
		}
	}
	guard, err := deps.beginRun(runCtx, plan.layout)
	if err != nil {
		return 0, fmt.Errorf("prepare native WSL runtime lease: %w", err)
	}
	containerGone := false
	defer func() {
		if err := guard.Close(containerGone); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("close native WSL runtime lease: %w", err))
		}
	}()
	container, err := deps.create(runCtx, runtimeSpec)
	if err != nil {
		return 0, fmt.Errorf("create native WSL tool container: %w", err)
	}
	if container.id == "" || container.runID == "" {
		return 0, errors.New("native WSL container creation returned an incomplete identity")
	}

	var (
		stream     attachStream
		running    bool
		term       terminalControl
		stopEvents func()
	)
	defer func() {
		cancelRun()
		if stream != nil {
			if err := stream.Close(); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("close native WSL attach stream: %w", err))
			}
		}
		if term.restore != nil {
			if err := term.restore(); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("restore native WSL terminal: %w", err))
			}
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		var lifecycleErr error
		if running {
			if err := deps.signal(cleanupCtx, container.id, 9); err != nil {
				lifecycleErr = fmt.Errorf("stop native WSL tool container after failure: %w", err)
			} else if _, err := deps.wait(cleanupCtx, container.id); err != nil {
				lifecycleErr = fmt.Errorf("wait for native WSL tool container after forced stop: %w", err)
			} else {
				running = false
			}
		}
		removed := false
		if running {
			// Start and wait failures can be transport-ambiguous. A proof-bound
			// non-force removal safely distinguishes an already-stopped container
			// from one that is still running without guessing or force-deleting it.
			if err := deps.remove(cleanupCtx, container); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("clean up ambiguously running native WSL tool container: %w", err))
			} else {
				removed = true
				containerGone = true
				running = false
			}
		}
		if !running && !removed {
			if err := deps.remove(cleanupCtx, container); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("clean up native WSL tool container: %w", err))
			} else {
				containerGone = true
			}
		}
		retErr = errors.Join(retErr, lifecycleErr)
		if stopEvents != nil {
			// Keep catchable host signals intercepted until proof-bound cleanup
			// finishes. Restoring their default disposition earlier can terminate
			// the shim inside the cleanup window and strand a retained container.
			stopEvents()
		}
	}()
	if err := guard.Adopt(container.runID); err != nil {
		return 0, fmt.Errorf("publish native WSL runtime lease: %w", err)
	}

	events, stop, err := deps.startEvents(runtimeSpec.TTY)
	if err != nil {
		return 0, err
	}
	stopEvents = stop
	stream, err = deps.attach(runCtx, wsldocker.AttachRequest{
		ContainerID: container.id, Stdin: true, Stdout: true, Stderr: true, TTY: runtimeSpec.TTY,
	})
	if err != nil {
		return 0, fmt.Errorf("attach native WSL tool container: %w", err)
	}
	term, err = deps.prepareTerminal(runtimeSpec.TTY)
	if err != nil {
		return 0, err
	}
	// Once start is submitted, its transport result cannot prove whether the
	// Engine acted. Treat the container as possibly running until a wait or the
	// proof-bound cleanup path establishes otherwise.
	running = true
	if err := deps.start(runCtx, container.id); err != nil {
		return 0, fmt.Errorf("start native WSL tool container: %w", err)
	}
	waitDone := make(chan waitResult, 1)
	go func() {
		waitCode, waitErr := deps.wait(runCtx, container.id)
		waitDone <- waitResult{code: waitCode, err: waitErr}
	}()
	if runtimeSpec.TTY && term.height != 0 && term.width != 0 {
		if err := deps.resize(runCtx, container.id, term.height, term.width); err != nil && !containerCompletionRace(err) {
			return 0, fmt.Errorf("set initial native WSL container terminal size: %w", err)
		}
	}

	outputDone := make(chan error, 1)
	go func() {
		if stream.Multiplexed() {
			_, err := wsldocker.CopyMultiplexedOutput(deps.stdout, deps.stderr, stream)
			outputDone <- err
			return
		}
		_, err := io.Copy(deps.stdout, stream)
		outputDone <- err
	}()
	inputDone := make(chan error, 1)
	go func() {
		inputDone <- copyToolInput(stream, deps.stdin)
	}()
	var outputResult *error
	for {
		select {
		case result := <-waitDone:
			if result.err != nil {
				return 0, fmt.Errorf("wait for native WSL tool container: %w", result.err)
			}
			running = false
			if outputResult != nil {
				if *outputResult != nil {
					return 0, fmt.Errorf("copy native WSL tool output: %w", *outputResult)
				}
				return finishToolResult(result.code, inputDone)
			}
			timer := time.NewTimer(outputDrainTimeout)
			select {
			case outputErr := <-outputDone:
				timer.Stop()
				if outputErr != nil {
					return 0, fmt.Errorf("copy native WSL tool output: %w", outputErr)
				}
				return finishToolResult(result.code, inputDone)
			case <-timer.C:
				return 0, errors.New("native WSL attach stream did not close after the container exited")
			case <-ctx.Done():
				timer.Stop()
				return 0, fmt.Errorf("native WSL tool execution canceled while draining output: %w", ctx.Err())
			}
		case outputErr := <-outputDone:
			outputResult = &outputErr
			outputDone = nil
			if outputErr != nil {
				return 0, fmt.Errorf("copy native WSL tool output: %w", outputErr)
			}
		case inputErr := <-inputDone:
			inputDone = nil
			if inputErr != nil && !errors.Is(inputErr, io.ErrClosedPipe) {
				return 0, fmt.Errorf("copy native WSL tool input: %w", inputErr)
			}
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if event.err != nil {
				return 0, event.err
			}
			if event.resize {
				if err := deps.resize(runCtx, container.id, event.height, event.width); err != nil && !containerCompletionRace(err) {
					return 0, fmt.Errorf("resize native WSL tool terminal: %w", err)
				}
			} else if event.signal != 0 {
				if err := deps.signal(runCtx, container.id, event.signal); err != nil && !containerCompletionRace(err) {
					return 0, fmt.Errorf("forward signal %d to native WSL tool container: %w", event.signal, err)
				}
			}
		case <-ctx.Done():
			return 0, fmt.Errorf("native WSL tool execution canceled: %w", ctx.Err())
		}
	}
}

type inputSinkWriter struct {
	destination io.Writer
}

func (w inputSinkWriter) Write(p []byte) (int, error) {
	written, err := w.destination.Write(p)
	// Only normalize errors observed at the attach sink. A source-side read
	// failure returned by io.Copy bypasses this wrapper and remains fatal.
	if inputPeerClosed(err) {
		return written, io.ErrClosedPipe
	}
	return written, err
}

func copyToolInput(stream attachStream, source io.Reader) error {
	_, copyErr := io.Copy(inputSinkWriter{destination: stream}, source)
	closeErr := stream.CloseWrite()
	if copyErr != nil {
		return copyErr
	}
	if inputPeerClosed(closeErr) {
		return io.ErrClosedPipe
	}
	return closeErr
}

func containerCompletionRace(err error) bool {
	var apiErr *wsldocker.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.StatusCode == http.StatusNotFound || apiErr.StatusCode == http.StatusConflict
}

func finishToolResult(code int, inputDone <-chan error) (int, error) {
	if inputDone == nil {
		return code, nil
	}
	select {
	case err := <-inputDone:
		if err != nil && !errors.Is(err, io.ErrClosedPipe) {
			return 0, fmt.Errorf("copy native WSL tool input: %w", err)
		}
	default:
		// A terminal or pipe reader can remain blocked after the tool exits.
		// Do not turn successful process completion into an unbounded stdin wait.
	}
	return code, nil
}
