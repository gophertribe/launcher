package launcher

import (
	"context"
	"fmt"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"sync"
	"syscall"
	"time"

	"os"
	"os/exec"
	"os/signal"
)

//RuntimeError wraps error exit code from the child process
type RuntimeError struct {
	ExitCode int
}

func (r *RuntimeError) Error() string {
	return fmt.Sprintf("child process exited with non-zero exit code: %d", r.ExitCode)
}

//Opts contains launcher configuration options
type Opts struct {
	InterruptTimeout time.Duration
}

var defaultOpts = &Opts{
	InterruptTimeout: 5 * time.Second,
}

type Launcher struct {
	sync.Mutex
	opts    *Opts
	binPath string
	cmd     *exec.Cmd
	sig     os.Signal
	quit    bool
}

func NewLauncher(binPath string) *Launcher {
	l := &Launcher{binPath: binPath}
	return l
}

func (l *Launcher) SetOpts(o *Opts) {
	l.opts = o
}

func (l *Launcher) Run(ctx context.Context, args ...string) error {
	if l.opts == nil {
		l.opts = defaultOpts
	}
	// extract logger if available
	logger := zerolog.Ctx(ctx)
	logger.UpdateContext(func(c zerolog.Context) zerolog.Context {
		return c.Str("executable", l.binPath)
	})

	for {
		if l.quit {
			logger.Info().Msgf("stopping launcher process")
			return nil
		}
		// run the child process
		l.sig = nil

		logger.Info().Msgf("executing command with arguments %s", args)
		l.cmd = exec.Command(l.binPath, args...)
		l.cmd.Stdin = os.Stdin
		l.cmd.Stdout = os.Stdout
		l.cmd.Stderr = os.Stderr
		err := l.cmd.Start()
		if err != nil {
			return errors.Wrapf(err, "error starting %s", l.binPath)
		}
		err = l.cmd.Wait()
		c := getExitCode(err)
		if l.sig == nil {
			if err != nil {
				logger.Error().Err(err).Msgf("child process %d exited with error code %d", l.cmd.Process.Pid, c)
			} else {
				logger.Warn().Msgf("child process %d exited with code %d", l.cmd.Process.Pid, c)
			}
			return &RuntimeError{c}
		}
		if err != nil {
			logger.Error().Err(err).Msgf("child process %d exited with code %d after receiving '%s' signal", l.cmd.Process.Pid, c, l.sig)
		} else {
			logger.Info().Msgf("child process %d exited with code %d after receiving '%s' signal", l.cmd.Process.Pid, c, l.sig)
		}
	}
}

func (l *Launcher) SignalHandler(ctx context.Context) {
	logger := zerolog.Ctx(ctx)
	// initialize os signal capturing logic
	sig := make(chan os.Signal)
	signal.Notify(sig)
	defer signal.Stop(sig)
	for {
		select {
		case s := <-sig:
			switch s {
			case os.Interrupt:
				l.quit = true
				fallthrough
			case syscall.SIGUSR1:
				// restarts the child
				logger.Info().Msgf("restarting child process %d", l.cmd.Process.Pid)
				c, cancel := context.WithTimeout(ctx, l.opts.InterruptTimeout)
				err := l.InterruptChild(c)
				if err != nil {
					logger.Error().Err(err).Msgf("error interrupting the process (exit code %d)", getExitCode(err))
				}
				cancel()
				return
			}
		case <-ctx.Done():
			logger.Info().Msg("signal processor will quit")
			return
		}
	}
}

func (l *Launcher) InterruptChild(ctx context.Context) error {
	// locks the launcher until the signal is not issued
	l.Lock()
	defer l.Unlock()
	logger := zerolog.Ctx(ctx)
	l.sig = os.Interrupt
	stop := make(chan error)
	go func(ret chan error) {
		ret <- l.cmd.Process.Signal(l.sig)
	}(stop)
	select {
	case err := <-stop:
		if err != nil {
			return errors.Wrap(err, "error encountered during child process interrupt")
		}
		return nil
	case <-ctx.Done():
		// proceed to kill
	}
	if !l.cmd.ProcessState.Exited() {
		logger.Warn().Msg("process did not exit gracefully; issuing a kill signal")
		err := l.doKillChild()
		if err != nil {
			return errors.Wrap(err, "error during child process kill")
		}
	}
	return nil
}

func (l *Launcher) KillChild() error {
	l.Lock()
	defer l.Unlock()
	return l.doKillChild()
}

func (l *Launcher) doKillChild() error {
	l.sig = os.Kill

	err := l.cmd.Process.Signal(l.sig)
	if err != nil {
		return err
	}
	return nil
}

func getExitCode(err error) int {
	if err == nil {
		return 0
	}
	if exiterr, ok := err.(*exec.ExitError); ok {
		if status, ok := exiterr.Sys().(syscall.WaitStatus); ok {
			return status.ExitStatus()
		}
	}
	return 99
}
