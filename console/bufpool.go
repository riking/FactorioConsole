package console

// if perf needed, switch to sync.Pool
type bufPool struct{}

var byteBufPool = bufPool{}

func (p *bufPool) Get() []byte {
	return make([]byte, 1024)
}

func (p *bufPool) Release(b []byte) {
}
