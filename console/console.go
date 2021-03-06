package console

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

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

var (
	colorStdout     = color.New(color.FgWhite)
	colorNotice     = color.New(color.FgHiWhite)
	colorBoldNotice = color.New(color.FgHiWhite, color.Bold)
	colorDebug      = color.New(color.FgHiBlack)
	colorChat       = color.New(color.FgHiBlue)
	colorCommand    = color.New(color.FgMagenta)
	colorGreen      = color.New(color.FgGreen)
	colorWarn       = color.New(color.FgYellow)
	colorStderr     = color.New(color.FgHiRed)
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

var regexpChatMessage = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d\d:\d\d:\d\d) \[(\w+)\] (.*)$`)
var regexpLogMessage = regexp.MustCompile(`^([ \d]+\.\d{3}) (.*)$`)
var regexpLoadingMod = regexp.MustCompile(`Loading mod ([a-zA-Z0-9 _-]+) (\d+\.\d+\.\d+) \(([\w_-]+\.lua)\)`)
var regexpMpManager = regexp.MustCompile(`Info MultiplayerManager.cpp:(\d+): (.*)$`)

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

	err = f.process.Start()
	if err != nil {
		return errors.Wrap(err, "starting Factorio process")
	}

	f.stopWg.Add(3)
	go f.runStdout(f.stdout)
	go f.runStdout(f.stderr)
	go f.runStdin()

	processExited := false
	didExit := waitForExit(f.process.Process)
	ourOutputBroken := false

	consoleWrite := func(w io.Writer, s string, c *color.Color) {
		err = fullyWrite(w, c.SprintlnFunc()(s))
		if err != nil {
			fmt.Println("output pipe broken", err)
			ourOutputBroken = true
		}
	}

	debugWrite := func(v ...interface{}) {
		fmt.Print(colorDebug.SprintlnFunc()(v...))
	}

	for !processExited {
		select {
		case <-didExit:
			// TODO timestamp?
			consoleWrite(f.console.Stderr(), "Factorio server exited", colorWarn)
			processExited = true
		case c := <-f.lineChan:
			switch c.ID {
			case controlMessageStdout:
				if m := regexpChatMessage.FindStringSubmatch(c.Data); m != nil {
					col := colorNotice // TODO colorChat
					if m[2] == "COMMAND" {
						col = colorCommand
					} else if m[2] == "WARNING" {
						col = colorWarn
					} else if m[2] == "CHAT" {
						col = colorChat
					}
					consoleWrite(f.console.Stdout(), c.Data, col)
				} else if m := regexpLogMessage.FindStringSubmatch(c.Data); m != nil {
					str := c.Data
					col := colorNotice
					if m2 := regexpMpManager.FindStringSubmatch(m[2]); m2 != nil {
						str = fmt.Sprintf("%s Info MultiplayerManager.cpp:%s: %s", m[1], m2[1],
							parseMpManagerLine(m, m2))
					} else if strings.HasPrefix(m[2], "Info ") {
						col = colorStdout
					} else if strings.HasPrefix(m[2], "Error") {
						col = colorWarn
					} else if strings.HasPrefix(m[2], "Checksum for") {
						col = colorDebug
					} else if strings.HasPrefix(m[2], "Hosting game at") {
						col = colorBoldNotice
					} else if m2 := regexpLoadingMod.FindStringSubmatch(m[2]); m2 != nil {
						col = colorStdout
						str = fmt.Sprintf("%s Loading mod %s %s (%s)", m[1],
							colorGreen.SprintFunc()(m2[1]),
							colorBoldNotice.SprintFunc()(m2[2]),
							colorDebug.SprintFunc()(m2[3]),
						)
					}
					consoleWrite(f.console.Stdout(), str, col)
				} else {
					consoleWrite(f.console.Stdout(), c.Data, colorStdout)
				}
			case controlMessageStderr:
				consoleWrite(f.console.Stderr(), c.Data, colorStderr)
			case controlMessageInput:
				err = f.sendCommand(c.Data)
				if err != nil {
					debugWrite("error sending to stdin:", err)
					debugWrite("did process exit?")
				}
			case controlMessageInputErr:
				if c.Extra == readline.ErrInterrupt || c.Extra == io.EOF {
					go f.StopServer()
					consoleWrite(f.console.Stderr(), "Caught ^C. Halting the server.", colorWarn)
				} else if c.Extra == errInputProbablyBroken {
					go f.StopServer()
					consoleWrite(f.console.Stderr(), "Exiting", colorWarn)
					closeConsole()
				}
			case controlMessageOutputErr:
				err = c.Extra
				debugWrite("output error:", err)
				debugWrite("did process exit?")
			default:
				debugWrite("unknown cmsg id", c)
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

	// Drain stdout, stderr
	drainDone := make(chan struct{})
	go func() {
		debugWrite("draining lineChan")
		defer debugWrite("drain done")
		for {
			select {
			case c := <-f.lineChan:
				switch c.ID {
				case controlMessageStdout:
					consoleWrite(f.console.Stdout(), c.Data, colorStdout)
				case controlMessageStderr:
					consoleWrite(f.console.Stderr(), c.Data, colorStderr)
				default:
					debugWrite("unexpected drain message: %T %#v\n", c, c)
				case controlInvalid:
					// zero read on closed channel
					return
				}
			case <-drainDone:
				return
			}
		}
	}()

	// Give a bit of time for stdout / stderr to finish
	time.Sleep(200 * time.Millisecond)

	// Signal to goroutines to exit
	debugWrite("closing stopChan")
	close(f.stopChan)

	// interrupt input reader by calling Close()
	debugWrite("closing console")
	closeConsole()

	// Wait for goroutines to exit
	debugWrite("wg.Wait")
	f.stopWg.Wait()
	close(drainDone)
	close(f.lineChan)

	// Fetch process return code, don't leave zombies
	debugWrite("collecting return code")
	return f.process.Wait()
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

func (f *Factorio) debugWrite(v ...interface{}) {
	fmt.Print(color.New(color.FgHiBlack).SprintlnFunc()(v...))
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
func (f *Factorio) runStdout(r io.ReadCloser) {
	defer f.stopWg.Done()
	defer f.debugWrite("runStdout returning")

	outReadCh, outResumeCh, outErrCh := readToChannel(r)
	var idMsg int
	if r == f.stdout {
		idMsg = controlMessageStdout
	} else {
		idMsg = controlMessageStderr
	}

	for {
		select {
		case b := <-outReadCh:
			f.lineChan <- control{ID: idMsg, Data: string(b)}
			outResumeCh <- struct{}{}
		case err := <-outErrCh:
			if err == io.EOF {
				return
			}
			f.lineChan <- control{ID: controlMessageOutputErr, Extra: err}
		}
	}
}

// runStdin owns os.Stdin (which is f.console)
func (f *Factorio) runStdin() {
	defer f.stopWg.Done()
	defer f.debugWrite("runStdin returning")

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
		} else {
			errChan <- io.EOF
		}
	}(r)
	return readChan, resumeChan, errChan
}

// we can't tell the difference between a ^D and input.Close()
// so if we get a bunch of EOFs, mark input as "probably broken"
func readlineToChannel(r *readline.Instance, stop chan struct{}) chan control {
	ch := make(chan control)

	go func(r *readline.Instance) {
		eofCount := 0
		for {
			str, err := r.Readline()
			if err == nil {
				eofCount = 0
				select {
				case ch <- control{ID: controlMessageInput, Data: str}:
					continue
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
					continue
				case <-stop:
					return
				}
			}
		}
	}(r)
	return ch
}
