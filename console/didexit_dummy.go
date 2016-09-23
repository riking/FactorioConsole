// +build darwin !unix
package console

import (
	"fmt"
	"os"
)

func waitForExit(p *os.Process) chan struct{} {
	ch := make(chan struct{})

	fmt.Println("OS X does not support WNOWAIT")
	close(ch)
	return ch
}
