package launcher

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"

	"log"

	"os"
	"os/exec"
	"os/signal"
)

var errNotInterrupted = errors.New("could not interrupt child process")

//RuntimeError wraps error exit code from the child process
type RuntimeError struct {
	ExitCode int
}

func (r RuntimeError) Error() string {
	return fmt.Sprintf("child process exited with non-zero exit code: %d", r.ExitCode)
}

func ExitCode(err error) int {
	if runtime, ok := err.(RuntimeError); ok {
		return runtime.ExitCode
	}
	return -1
}

func Launch(ctx context.Context, interruptSignal os.Signal, interruptTimeout time.Duration, binPath string, args ...string) error {
	// out will receive child signal exit code
	out := make(chan int)
	defer close(out)

	// sig receives system signals
	sig := make(chan os.Signal)
	signal.Notify(sig)
	defer signal.Stop(sig)

	// we loop until the launcher receives an interrupt signal
RUN:
	for {
		cmd := exec.Command(binPath, args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Start()
		if err != nil {
			return fmt.Errorf("error starting %s: %w", binPath, err)
		}

		// wait for the child to terminate or for system signals to come
		go waitForChild(cmd, out)

		for {
			select {
			case code := <-out:
				// child process exited itself, here this is an unexpected event
				return fmt.Errorf("child process exited unexpectedly with exit code: %d", code)
			case s := <-sig:
				switch s {
				case os.Interrupt:
					// we simply stop the child and exit
					return interruptChild(ctx, cmd, out, interruptSignal, interruptTimeout)
				case syscall.SIGUSR1:
					err := interruptChild(ctx, cmd, out, interruptSignal, interruptTimeout)
					// if the child process could not be interrupted it's safer to exit immediately
					if errors.Is(err, errNotInterrupted) {
						return err
					}
					if runtime, ok := err.(RuntimeError); ok {
						log.Printf("child process returned non-zero exit code (%d) during restart", runtime.ExitCode)
					}
					continue RUN
				}
			case <-ctx.Done():
				// same as simple interrupt
				return interruptChild(ctx, cmd, out, interruptSignal, interruptTimeout)
			}
		}
	}
}

func interruptChild(ctx context.Context, cmd *exec.Cmd, out <-chan int, signal os.Signal, timeout time.Duration) error {
	// issue an interrupt signal and wait for the command to end
	err := cmd.Process.Signal(signal)
	if err != nil {
		if cmd.ProcessState.Exited() {
			if code := cmd.ProcessState.ExitCode(); code != 0 {
				return RuntimeError{ExitCode: code}
			}
			return nil
		}
		return fmt.Errorf("could not send interrupt signal to child process: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case code := <-out:
		if code != 0 {
			return RuntimeError{ExitCode: code}
		}
		return nil
	case <-ctx.Done():
		if !cmd.ProcessState.Exited() {
			log.Printf("child process %s did not exit gracefully; issuing a kill signal", cmd.Path)
			err = cmd.Process.Signal(os.Kill)
			if err != nil {
				return fmt.Errorf("could not send kill signal to child process: %w", err)
			}
			// now it must exit
			code := <-out
			return RuntimeError{ExitCode: code}
		}
		// if the process has exit somehow we check its exit code
		if code := cmd.ProcessState.ExitCode(); code != 0 {
			return RuntimeError{ExitCode: code}
		}
		return nil
	}
}

func waitForChild(cmd *exec.Cmd, out chan<- int) {
	err := cmd.Wait()
	if err == nil {
		out <- 0
		return
	}
	if exit, ok := err.(*exec.ExitError); ok {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok {
			out <- status.ExitStatus()
			return
		}
	}
	out <- 99
}
