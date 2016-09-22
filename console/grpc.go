package console

import (
	"context"
	p "github.com/riking/FactorioConsole/proto"
)

func (f *Factorio) LuaCommand(ctx context.Context, req *p.LuaCommandRequest) (*p.CommandResponse, error) {
	// TODO
	return nil, nil
}

func (f *Factorio) OtherCommand(ctx context.Context, req *p.CommandRequest) (*p.CommandResponse, error) {
	// TODO
	return nil, nil
}
