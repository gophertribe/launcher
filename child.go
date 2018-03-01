package launcher

import (
	"fmt"
	"os"
	"syscall"
)

//ErrNoParent is returned if the current process has no parent (it's ppid equals 1)
var ErrNoParent = fmt.Errorf("running without parent launcher process")

//RequestRestart sends a SIGUSR1 signal to the parent launcher process causing it to
//restart the requestor (child process). Returns ErrNoParent if there is no parent.
func RequestRestart() error {
	ppid := os.Getppid()
	if ppid == 1 {
		return ErrNoParent
	}
	pp, err := os.FindProcess(ppid)
	if err != nil {
		return err
	}
	return pp.Signal(syscall.SIGUSR1)
}
