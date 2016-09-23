// +build darwin
package console

import (
	"os"
)

func waitForExit(p *os.Process) chan struct{} {
	ch := make(chan struct{})

	panic("OS X does not support WNOWAIT")

	close(ch)
	return ch
}
