package internal

import "os"

type Process struct {
	Pid int
}

func (p *Process) BlockUntilWaitable() (bool, error) {
	return p.blockUntilWaitable()
}

func NewSyscallError(syscall string, err error) error {
	return os.NewSyscallError(syscall, err)
}
