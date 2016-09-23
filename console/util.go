package console

import (
	"sync"
)

func wrapOnce(f func()) func() {
	var o sync.Once
	return func() {
		o.Do(f)
	}
}
