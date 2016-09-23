package main

import (
	"fmt"
	"os"

	"github.com/chzyer/readline"
	"github.com/pkg/errors"
	"github.com/riking/FactorioConsole/console"
)

func main() {
	c := console.Config{}
	c.Args = os.Args[1:]
	rl := &readline.Config{}
	err := rl.Init()
	if err != nil {
		fmt.Fprintln(os.Stderr, errors.Wrap(err, "readline setup"))
	}
	rl.Prompt = "> "
	c.RLConfig = rl

	term := console.Factorio{}
	err = term.Start(&c)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}
