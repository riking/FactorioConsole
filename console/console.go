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
	console *readline.Instance
	process *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	stderr  io.ReadCloser

	// lineChan is sent from the stdin, stdout, and stderr goroutines to the
	// coordinator goroutine
	lineChan chan (control)
	// stopChan is closed by the coordinator goroutine to tell the stdin,
	// stdout, and stderr goroutines to shut down
	stopChan chan (struct{})
	stopWg   sync.WaitGroup

	RPCChan chan Request
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
	// controlMessageInputErr has either io.EOF or readline.ErrInterrupt in Extra.
	// if Extra is readline.ErrInterrupt, Data has the partial string
	controlMessageInputErr
	// controlMessageOutputErr occurs when reading from stdout or stderr fails.
	// This probably means the process has exited, so start shutdown procedures.
	// The error value is in Extra.
	controlMessageOutputErr
)

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
	f.console, err = readline.NewEx(c.RLConfig)
	if err != nil {
		return errors.Wrap(err, "starting readline - is your terminal set up wrong?")
	}
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
	f.RPCChan = make(chan Request)
	return nil
}

func (f *Factorio) Run() error {
	f.stopWg.Add(2)
	go f.runStdout()
	go f.runStdin()

	var err error
	processExited := false
	ourOutputBroken := false
	hadCtrlC := false

	consoleWrite := func(w io.Writer, s string) {
		err = fullyWrite(w, s)
		if err != nil {
			fmt.Println("output pipe broken")
			ourOutputBroken = true
		}
	}

	for {
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
				}
			case controlMessageOutputErr:
				err = c.Extra
				fmt.Println("output error:", err)
				fmt.Println("marking process as exited")
				processExited = true
			}
		case r := <-f.RPCChan:
			// TODO
			_ = r
		}
		if processExited {
			// Signal to goroutines to exit
			close(f.stopChan)
			// interrupt input reader by calling Close()
			f.console.Close()
			// Wait for goroutines to exit
			f.stopWg.Wait()
			break
		}
		if ourOutputBroken {
			go f.StopServer()
		}
	}
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
	bw := bufio.NewWriter(w)
	_, err := bw.WriteString(s)
	if err != nil {
		return err
	}
	return bw.Flush()
}

// runStdout owns f.stdout and f.stderr
func (f *Factorio) runStdout() {
	defer f.stopWg.Done()

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

// runStdin owns os.Stdin (which is f.console)
func (f *Factorio) runStdin() {
	defer f.stopWg.Done()

	for {
		str, err := f.console.Readline()
		if err == nil {
			f.lineChan <- control{ID: controlMessageInput, Data: str}
		} else {
			f.lineChan <- control{ID: controlMessageInputErr, Data: str, Extra: err}
		}
		select {
		case <-f.stopChan:
			return
		default:
			// loop
		}
	}
}
