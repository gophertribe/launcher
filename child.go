package launcher

import (
	"fmt"
	"log"
	"os"
	"syscall"
)

// ErrNoParent is returned if the current process has no parent (it's ppid equals 1)
var ErrNoParent = fmt.Errorf("running without parent launcher process")

// RequestRestart sends a SIGUSR1 signal to the parent launcher process causing it to
// restart controlled child process. Returns ErrNoParent if there is no parent.
func RequestRestart() error {
	pp, err := os.FindProcess(os.Getppid())
	if err != nil {
		return err
	}
	if pp == nil {
		return ErrNoParent
	}
	log.Printf("sending %s signal to PID %d", syscall.SIGUSR1, pp.Pid)
	return pp.Signal(syscall.SIGUSR1)
}
