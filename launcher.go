package launcher

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

var errNotInterrupted = errors.New("could not interrupt child process")

// RuntimeError wraps error exit code from the child process
type RuntimeError struct {
	ExitCode int
}

func (r RuntimeError) Error() string {
	return fmt.Sprintf("child process exited with non-zero exit code: %d", r.ExitCode)
}

func ExitCode(err error) int {
	var runtime RuntimeError
	if errors.As(err, &runtime) {
		return runtime.ExitCode
	}
	return 1
}

type Opt func(*Opts)

func WithInterruptSignal(signal os.Signal) Opt {
	return func(o *Opts) {
		o.InterruptSignal = signal
	}
}

func WithInterruptTimeout(timeout time.Duration) Opt {
	return func(o *Opts) {
		o.InterruptTimeout = timeout
	}
}

func WithWaitGroup(wg *sync.WaitGroup) Opt {
	return func(o *Opts) {
		o.WaitGroup = wg
	}
}

type Opts struct {
	InterruptSignal  os.Signal
	InterruptTimeout time.Duration
	WaitGroup        *sync.WaitGroup
}

type Launcher struct {
	child *exec.Cmd
	opts  Opts
}

func New(opt ...Opt) *Launcher {
	launcher := &Launcher{
		opts: Opts{
			InterruptSignal:  os.Interrupt,
			InterruptTimeout: 10 * time.Second,
			WaitGroup:        &sync.WaitGroup{},
		},
	}
	for _, o := range opt {
		o(&launcher.opts)
	}
	return launcher
}

func (launcher *Launcher) Launch(ctx context.Context, binPath string, args ...string) error {
	// out will receive child signal exit code
	out := make(chan int)
	defer close(out)

	// sig receives system signals
	sig := make(chan os.Signal, 1)
	signal.Notify(sig)
	defer signal.Stop(sig)

	// we loop until the launcher receives an interrupt signal
RUN:
	for {
		launcher.child = exec.CommandContext(ctx, binPath, args...)
		launcher.child.Stdin = os.Stdin
		launcher.child.Stdout = os.Stdout
		launcher.child.Stderr = os.Stderr
		err := launcher.child.Start()
		if err != nil {
			return fmt.Errorf("error starting %s: %w", binPath, err)
		}
		slog.Info("started child process", "cmd", binPath, "args", args, "pid", launcher.child.Process.Pid)

		// wait for the child to terminate or for system signals to come
		launcher.waitForChild(out)

		for {
			select {
			case code := <-out:
				// child process exited itself, here this is an unexpected event
				return fmt.Errorf("child process exited unexpectedly with exit code: %d", code)
			case s := <-sig:
				switch s {
				case os.Interrupt, os.Kill, syscall.SIGTERM, syscall.SIGQUIT:
					slog.Info("signal received; interrupting child process", "signal", s)
					// we simply stop the child and exit
					return launcher.interruptChild(ctx, out)
				case syscall.SIGUSR1:
					slog.Info("signal received; restarting child process", "signal", s)
					err := launcher.interruptChild(ctx, out)
					// if the child process could not be interrupted it's safer to exit immediately
					if errors.Is(err, errNotInterrupted) {
						return err
					}
					var runtime RuntimeError
					if errors.As(err, &runtime) {
						log.Printf("child process returned non-zero exit code (%d) during restart", runtime.ExitCode)
					}
					continue RUN
				}
			case <-ctx.Done():
				// same as simple interrupt
				return launcher.interruptChild(ctx, out)
			}
		}
	}
}

func (launcher *Launcher) interruptChild(ctx context.Context, out <-chan int) error {
	// issue an interrupt signal and wait for the command to end
	err := launcher.child.Process.Signal(launcher.opts.InterruptSignal)
	if err != nil {
		if launcher.child.ProcessState.Exited() {
			if code := launcher.child.ProcessState.ExitCode(); code != 0 {
				return RuntimeError{ExitCode: code}
			}
			return nil
		}
		return fmt.Errorf("%w: could not send interrupt signal to child process: %v", errNotInterrupted, err)
	}
	ctx, cancel := context.WithTimeout(ctx, launcher.opts.InterruptTimeout)
	defer cancel()

	select {
	case code := <-out:
		if code != 0 {
			return RuntimeError{ExitCode: code}
		}
		return nil
	case <-ctx.Done():
		if launcher.child.ProcessState == nil {
			return ctx.Err()
		}
		if !launcher.child.ProcessState.Exited() {
			slog.Info("child process did not exit gracefully; issuing a kill signal", "cmd", launcher.child.Path, "pid", launcher.child.Process.Pid)
			err = launcher.child.Process.Signal(syscall.SIGTERM)
			if err != nil {
				return fmt.Errorf("%w: could not send kill signal to child process: %v", errNotInterrupted, err)
			}
			// now it must exit
			code := <-out
			return RuntimeError{ExitCode: code}
		}
		// if the process has exit somehow we check its exit code
		if code := launcher.child.ProcessState.ExitCode(); code != 0 {
			return RuntimeError{ExitCode: code}
		}
		return ctx.Err()
	}
}

func (launcher *Launcher) waitForChild(out chan<- int) {
	launcher.opts.WaitGroup.Add(1)
	go func() {
		defer launcher.opts.WaitGroup.Done()
		err := launcher.child.Wait()
		if err == nil {
			out <- 0
			return
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if status, ok := exit.Sys().(syscall.WaitStatus); ok {
				out <- status.ExitStatus()
				return
			}
		}
		out <- 1
	}()
}
