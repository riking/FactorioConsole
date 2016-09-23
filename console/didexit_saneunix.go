// +build linux freebsd,!darwin

package console

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func waitForExit(p *os.Process) chan struct{} {
	ch := make(chan struct{})

	go func(p *os.Process, ch chan struct{}) {
		var wstatus unix.WaitStatus
		for {
			p, err := unix.Wait4(p.Pid, &wstatus, 0 /*unix.WNOWAIT*/, nil)
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				fmt.Println("wait4 error:", p, err)
			}
			break
		}
		close(ch)
	}(p, ch)

	return ch
}
