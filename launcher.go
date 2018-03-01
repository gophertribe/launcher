package launcher

import (
	"fmt"
	"sync"
	"syscall"
	"time"

	"os"
	"os/exec"
	"os/signal"

	log "github.com/sirupsen/logrus"
)

//RuntimeError wraps error exit code from the child process
type RuntimeError struct {
	ExitCode int
}

func (r *RuntimeError) Error() string {
	return fmt.Sprintf("child process exited with non-zero exit code %d", r.ExitCode)
}

//Opts contains launcher configuration options
type Opts struct {
	InterruptTimeout time.Duration
}

var logger = log.New()

var defaultOpts = &Opts{
	InterruptTimeout: 5 * time.Second,
}

func init() {
	logger.SetLevel(log.InfoLevel)
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

func SetLogLevel(level int) {
	logger.SetLevel(log.Level(level))
}

func (l *Launcher) SetOpts(o *Opts) {
	l.opts = o
}

func (l *Launcher) Run(args ...string) error {
	if l.opts == nil {
		l.opts = defaultOpts
	}

	for {
		// run the child process
		if l.quit {
			break
		}
		l.sig = nil

		logger.Infof("executing command %s with arguments %s", l.binPath, args)
		l.cmd = exec.Command(l.binPath, args...)
		l.cmd.Stdin = os.Stdin
		l.cmd.Stdout = os.Stdout
		l.cmd.Stderr = os.Stderr
		err := l.cmd.Start()
		if err != nil {
			return fmt.Errorf("error starting %s: %s", l.binPath, err.Error())
		}
		err = l.cmd.Wait()
		c := getExitCode(err)
		if l.sig == nil {
			if err != nil {
				logger.Errorf("child process %d exited with error (code %d): %s", l.cmd.Process.Pid, c, err.Error())
			} else {
				logger.Warnf("child process %d exited with code %d", l.cmd.Process.Pid, c)
			}
			return &RuntimeError{c}
		}
		if err != nil {
			logger.Errorf("child process %d exited with code %d after receiving '%s' signal (%s)", l.cmd.Process.Pid, c, l.sig, err.Error())
		} else {
			logger.Infof("child process %d exited with code %d after receiving '%s' signal", l.cmd.Process.Pid, c, l.sig)
		}
	}
	return nil
}

func (l *Launcher) SignalHandler() {
	// initialize os signal capturing logic
	sig := make(chan os.Signal)
	signal.Notify(sig)
	defer signal.Stop(sig)
	for s := range sig {
		switch s {
		case os.Interrupt:
			l.quit = true
			err := l.InterruptChild()
			if err != nil {
				logger.Errorf("error interrupting the process (exit code %d): %s", getExitCode(err), err.Error())
			}
			return
		case syscall.SIGUSR1:
			// restarts the child
			logger.Infof("restarting child process %d", l.cmd.Process.Pid)
			err := l.InterruptChild()
			if err != nil {
				logger.Errorf("error interrupting the process (exit code %d): %s", getExitCode(err), err.Error())
			}
		}
	}
}

func (l *Launcher) InterruptChild() error {
	// locks the launcher until the signal is not issued
	l.Lock()
	defer l.Unlock()
	l.sig = os.Interrupt
	stop := make(chan error)
	go func(ret chan error) {
		ret <- l.cmd.Process.Signal(l.sig)
	}(stop)
	select {
	case err := <-stop:
		if err != nil {
			logger.Errorf("error encountered during child process interrupt: %s", err.Error())
			return err
		}
		return nil
	case <-time.After(l.opts.InterruptTimeout):
		// proceed to kill
	}
	if !l.cmd.ProcessState.Exited() {
		logger.Warn("process did not exit gracefully; issuing a kill signal")
		return l.doKillChild()
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
		logger.Errorf("error encountered during child process kill: %s", err.Error())
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
