// +build linux freebsd,!darwin

package console

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

/*
#include <sys/types.h>
#include <sys/wait.h>
*/
import "C"

func waitForExit(p *os.Process) chan struct{} {
	ch := make(chan struct{})

	go func(p *os.Process, ch chan struct{}) {
		var wstatus C.siginfo_t
		for {
			r1, r2, err := unix.RawSyscall(unix.SYS_WAITID, C.P_PID, p.Pid, unsafe.Pointer(&wstatus), C.WNOWAIT)
			if err == unix.EINTR {
				continue
			}
			fmt.Println("waitid return:", r1, r2, err)
			if err != nil {
				fmt.Println("wait4 error:", p, err)
			}
			break
		}
		close(ch)
	}(p, ch)

	return ch
}
