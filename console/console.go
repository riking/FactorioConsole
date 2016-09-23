package console

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/chzyer/readline"
	"github.com/fatih/color"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
)

type Config struct {
	Args     []string
	GRPC     *grpc.Server
	RLConfig *readline.Config
}

type Factorio struct {
	rlConfig *readline.Config
	console  *readline.Instance
	process  *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	stderr   io.ReadCloser

	// lineChan is sent from the stdin, stdout, and stderr goroutines to the
	// coordinator goroutine
	lineChan chan (control)
	// stopChan is closed by the coordinator goroutine to tell the stdin,
	// stdout, and stderr goroutines to shut down
	stopChan chan (struct{})
	stopWg   sync.WaitGroup
	// rpcChan is written from the GRPC thread
	rpcChan chan Request
}

type Request struct {
	IsLua   bool
	Command string
	Return  chan string
}

func NewRequest(command string, lua bool) Request {
	return Request{
		IsLua:   lua,
		Command: command,
		Return:  make(chan string),
	}
}

// control is used to pass messages to the coordinator
type control struct {
	ID    int
	Data  string
	Extra error
}

const (
	controlInvalid = iota
	// controlQuit means that the server is shutting down.
	controlQuit
	// controlMessage{Stdout,Stderr,Input} have the string in Data.
	controlMessageStdout
	controlMessageStderr
	controlMessageInput
	// controlMessageInputErr has either io.EOF, readline.ErrInterrupt, or
	// errInputProbablyBroken in Extra.
	//
	// if Extra is readline.ErrInterrupt, Data has the partial string
	controlMessageInputErr
	// controlMessageOutputErr occurs when reading from stdout or stderr fails.
	// This probably means the process has exited, so start shutdown procedures.
	// The error value is in Extra.
	controlMessageOutputErr
)

var errInputProbablyBroken = errors.Errorf("got multiple io.EOFs in row")

const programName = `bin/x64/factorio`

// Start runs the Factorio server and blocks until the server exits.
func (f *Factorio) Start(c *Config) error {
	err := f.Setup(c)
	if err != nil {
		return err
	}
	return f.Run()
}

// Setup prepares readline and the process for a call to Run().
func (f *Factorio) Setup(c *Config) error {
	var err error
	f.rlConfig = c.RLConfig
	f.process = exec.Command(programName, c.Args...)
	f.stdin, err = f.process.StdinPipe()
	if err != nil {
		return errors.Wrap(err, "creating pipes")
	}
	f.stdout, err = f.process.StdoutPipe()
	if err != nil {
		return errors.Wrap(err, "creating pipes")
	}
	f.stderr, err = f.process.StderrPipe()
	if err != nil {
		return errors.Wrap(err, "creating pipes")
	}
	f.lineChan = make(chan control)
	f.stopChan = make(chan struct{})
	f.rpcChan = make(chan Request)
	// TODO grpc
	return nil
}

func (f *Factorio) Run() error {
	var err error
	f.console, err = readline.NewEx(f.rlConfig)
	if err != nil {
		return errors.Wrap(err, "starting readline - is your terminal set up wrong?")
	}
	closeConsole := wrapOnce(func() {
		f.console.Close()
	})
	defer closeConsole()

	ourOutputBroken := false

	consoleWrite := func(w io.Writer, s string) {
		s = strings.TrimRight(s, "\n") + "\n"
		err = fullyWrite(w, s)
		if err != nil {
			fmt.Println("output pipe broken")
			ourOutputBroken = true
		}
	}

	err = f.process.Start()
	if err != nil {
		return errors.Wrap(err, "starting Factorio process")
	}
	processExited := false

	f.stopWg.Add(2)
	go f.runStdout()
	go f.runStdin()

	hadCtrlC := false

	for !processExited {
		select {
		case c := <-f.lineChan:
			switch c.ID {
			case controlMessageStdout:
				consoleWrite(f.console.Stdout(), color.WhiteString(c.Data))
			case controlMessageStderr:
				consoleWrite(f.console.Stderr(), color.RedString(c.Data))
			case controlMessageInput:
				hadCtrlC = false
				err = f.sendCommand(c.Data)
				if err != nil {
					fmt.Println("error sending to stdin")
					fmt.Println("marking process as exited")
					processExited = true
				}
			case controlMessageInputErr:
				// TODO verify
				if c.Extra == readline.ErrInterrupt {
					if hadCtrlC {
						go f.StopServer()
					} else {
						consoleWrite(f.console.Stderr(), color.YellowString("Use 'stop' or ^C again to halt the server.\n"))
						hadCtrlC = true
					}
				} else if c.Extra == io.EOF {
					consoleWrite(f.console.Stderr(), color.YellowString("got EOF, ignoring\n"))
					// TODO verify
					// ignore
				} else if c.Extra == errInputProbablyBroken {
					consoleWrite(f.console.Stderr(), color.YellowString("Exiting\n"))
					closeConsole()
					go f.StopServer()
				}
			case controlMessageOutputErr:
				err = c.Extra
				fmt.Println("output error:", err)
				fmt.Println("marking process as exited")
				processExited = true
			}
		case r := <-f.rpcChan:
			// TODO
			_ = r
			r.Return <- "ErrNotImplemented"
		}
		if ourOutputBroken {
			go f.StopServer()
		}
	}

	// Signal to goroutines to exit
	fmt.Println("closing stopChan")
	close(f.stopChan)

	// Drain lineChan
	drainDone := make(chan struct{})
	go func() {
		fmt.Println("draining lineChan")
		defer fmt.Println("drain done")
		for {
			select {
			case c := <-f.lineChan:
				switch c.ID {
				case controlMessageStdout:
					consoleWrite(f.console.Stdout(), color.WhiteString(c.Data))
				case controlMessageStderr:
					consoleWrite(f.console.Stderr(), color.RedString(c.Data))
				default:
					fmt.Println("unexpected drain message", c)
				}
			case <-drainDone:
				fmt.Println("linechan drain done")
				return
			}
		}
	}()

	// interrupt input reader by calling Close()
	fmt.Println("closing console")
	closeConsole()

	// Wait for goroutines to exit
	fmt.Println("wg.Wait")
	f.stopWg.Wait()
	close(drainDone)
	close(f.lineChan)

	// Fetch process return code, don't leave zombies
	fmt.Println("collecting return code")
	fmt.Println(f.process.Wait())
	return nil
}

// StopServer closes the factorio binary's stdin and sends it a SIGINT.
func (f *Factorio) StopServer() {
	f.stdin.Close()
	f.process.Process.Signal(os.Interrupt)
}

func (f *Factorio) sendCommand(cmd string) error {
	cmd = strings.TrimRight(cmd, "\n") + "\n"
	return fullyWrite(f.stdin, cmd)
}

func fullyWrite(w io.Writer, s string) error {
	b := []byte(s)
	lenLeft := len(s)
	offset := 0
	for lenLeft > 0 {
		n, err := w.Write(b[offset:])
		if err != nil {
			return err
		}
		lenLeft -= n
		offset += n
	}
	return nil
}

// runStdout owns f.stdout and f.stderr
func (f *Factorio) runStdout() {
	defer f.stopWg.Done()
	defer fmt.Println("runStdout returning")

	outReadCh, outResumeCh, outErrCh := readToChannel(f.stdout)
	serReadCh, serResumeCh, serErrCh := readToChannel(f.stderr)

	for {
		select {
		case <-f.stopChan:
			return
		case b := <-outReadCh:
			f.lineChan <- control{ID: controlMessageStdout, Data: string(b)}
			outResumeCh <- struct{}{}
		case b := <-serReadCh:
			f.lineChan <- control{ID: controlMessageStderr, Data: string(b)}
			serResumeCh <- struct{}{}
		case err := <-outErrCh:
			f.lineChan <- control{ID: controlMessageOutputErr, Extra: err}
		case err := <-serErrCh:
			f.lineChan <- control{ID: controlMessageOutputErr, Extra: err}
		}
	}
}

// runStdin owns os.Stdin (which is f.console)
func (f *Factorio) runStdin() {
	defer f.stopWg.Done()
	defer fmt.Println("runStdin returning")

	ch := readlineToChannel(f.console, f.stopChan)
	for {
		select {
		case msg := <-ch:
			// Split this into two selects so that stopChan works
			// if we drop a line, that's not a big deal
			select {
			case f.lineChan <- msg:
			case <-f.stopChan:
				return
			}
		case <-f.stopChan:
			return
		}
	}
}

// readToChannel splits the reader into lines and sends each line down bytesCh
// on a new goroutine.
//
// After receiving over bytesCh, the receiver must process the data and send an
// empty struct on resumeCh, which is the signal that the provided bytes array
// may be reused.
//
// At io.EOF or any other error, the error will be sent on errCh and the
// goroutine will exit.
func readToChannel(r io.Reader) (bytesCh <-chan []byte, resumeCh chan<- struct{}, errCh <-chan error) {
	defer fmt.Println("readToChannel returning")

	readChan := make(chan []byte)
	resumeChan := make(chan struct{})
	errChan := make(chan error)
	go func(r io.Reader) {
		s := bufio.NewScanner(r)
		for s.Scan() {
			readChan <- s.Bytes()
			<-resumeChan
		}
		if s.Err() != nil {
			errChan <- s.Err()
		}
	}(r)
	return readChan, resumeChan, errChan
}

// we can't tell the difference between a ^D and input.Close()
// so if we get a bunch of EOFs, mark input as "probably broken"
func readlineToChannel(r *readline.Instance, stop chan struct{}) chan control {
	defer fmt.Println("readlineToChannel returning")

	ch := make(chan control)
	go func(r *readline.Instance) {
		eofCount := 0
		for {
			str, err := r.Readline()
			if err == nil {
				eofCount = 0
				select {
				case ch <- control{ID: controlMessageInput, Data: str}:
				case <-stop:
					return
				}
			} else {
				if err == io.EOF {
					eofCount++
					if eofCount > 4 {
						err = errInputProbablyBroken
					}
				}
				select {
				case ch <- control{ID: controlMessageInputErr, Data: str, Extra: err}:
				case <-stop:
					return
				}
			}
		}
	}(r)
	return ch
}
