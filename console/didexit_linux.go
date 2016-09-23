//+build linux
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
			_, err := unix.Wait4(p.Pid, &wstatus, unix.WNOWAIT, nil)
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				fmt.Println(err)
			}
			break
		}
		close(ch)
	}(p, ch)

	return ch
}
