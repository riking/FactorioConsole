package console

import (
	"fmt"
	"os"
	"sync"

	"github.com/riking/FactorioConsole/console/internal"
)

func wrapOnce(f func()) func() {
	var o sync.Once
	return func() {
		o.Do(f)
	}
}

func waitForExit(p *os.Process) chan struct{} {
	ch := make(chan struct{})
	pr := internal.Process{Pid: p.Pid}
	go func() {
		b, err := pr.BlockUntilWaitable()
		fmt.Println("blockUntilWaitable:", b, err)
		close(ch)
	}()
	return ch
}
