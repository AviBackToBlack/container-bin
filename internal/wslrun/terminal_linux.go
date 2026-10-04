//go:build linux

package wslrun

import (
	"errors"
	"fmt"
	"os"
	ossignal "os/signal"
	"sync"
	"syscall"
	"unsafe"
)

func prepareHostTerminal(tty bool) (terminalControl, error) {
	if !tty {
		return terminalControl{restore: func() error { return nil }}, nil
	}
	stdinFD := os.Stdin.Fd()
	original, err := getTermios(stdinFD)
	if err != nil {
		return terminalControl{}, fmt.Errorf("inspect native WSL terminal mode: %w", err)
	}
	raw := original
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if err := setTermios(stdinFD, raw); err != nil {
		return terminalControl{}, fmt.Errorf("enter native WSL raw terminal mode: %w", err)
	}
	height, width, err := terminalSize(stdinFD)
	if err != nil {
		_ = setTermios(stdinFD, original)
		return terminalControl{}, fmt.Errorf("read native WSL terminal size: %w", err)
	}
	var (
		once       sync.Once
		restoreErr error
	)
	return terminalControl{
		height: height,
		width:  width,
		restore: func() error {
			once.Do(func() { restoreErr = setTermios(stdinFD, original) })
			return restoreErr
		},
	}, nil
}

// interactiveHostTerminal requires a real Linux terminal on stdin. Character
// device mode alone is insufficient because /dev/null and /dev/zero also set
// os.ModeCharDevice but reject terminal ioctls. Stdout may be redirected; TTY
// sizing is taken from the controlling stdin terminal.
func interactiveHostTerminal() bool {
	_, err := getTermios(os.Stdin.Fd())
	return err == nil
}

func startHostEvents(tty bool) (<-chan hostEvent, func(), error) {
	signals := make(chan os.Signal, 16)
	watched := []os.Signal{
		syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGUSR1,
		syscall.SIGUSR2, syscall.SIGTERM, syscall.SIGCONT, syscall.SIGTSTP,
		syscall.SIGPIPE,
	}
	if tty {
		watched = append(watched, syscall.SIGWINCH)
	}
	ossignal.Notify(signals, watched...)
	events := make(chan hostEvent, 16)
	done := make(chan struct{})
	var once sync.Once
	stop := func() {
		once.Do(func() {
			ossignal.Stop(signals)
			close(done)
		})
	}
	go func() {
		defer close(events)
		for {
			select {
			case <-done:
				return
			case received := <-signals:
				number, ok := received.(syscall.Signal)
				if !ok {
					select {
					case events <- hostEvent{err: errors.New("native WSL received a signal without a Linux number")}:
					case <-done:
					}
					continue
				}
				event := hostEvent{signal: int(number)}
				if number == syscall.SIGWINCH {
					height, width, err := terminalSize(os.Stdin.Fd())
					event = hostEvent{resize: true, height: height, width: width}
					if err != nil {
						event = hostEvent{err: fmt.Errorf("read resized native WSL terminal: %w", err)}
					}
				}
				select {
				case events <- event:
				case <-done:
					return
				}
			}
		}
	}()
	return events, stop, nil
}

func getTermios(fd uintptr) (syscall.Termios, error) {
	var value syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&value)), 0, 0, 0)
	if errno != 0 {
		return syscall.Termios{}, errno
	}
	return value, nil
}

func setTermios(fd uintptr, value syscall.Termios) error {
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&value)), 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func terminalSize(fd uintptr) (uint16, uint16, error) {
	// Linux struct winsize is four consecutive unsigned shorts. Keep the
	// definition local rather than adding x/sys solely for one ioctl.
	var size struct {
		Row    uint16
		Col    uint16
		Xpixel uint16
		Ypixel uint16
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, fd, syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&size)), 0, 0, 0)
	if errno != 0 {
		return 0, 0, errno
	}
	if size.Row == 0 || size.Col == 0 {
		return 0, 0, errors.New("terminal reported zero rows or columns")
	}
	return size.Row, size.Col, nil
}
