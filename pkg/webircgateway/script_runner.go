package webircgateway

import (
	"sync"

	"github.com/aarzilli/golua/lua"
)

// ScriptRunner - Execute embedded Lua scripts
type ScriptRunner struct {
	gateway *Gateway
	sync.Mutex
	L *lua.State
}

// NewScriptRunner - Create a new ScriptRunner
func NewScriptRunner(g *Gateway) *ScriptRunner {
	runner := &ScriptRunner{}
	runner.gateway = g
	runner.L = lua.NewState()
	runner.L.OpenLibs()

	runner.L.Register("client_write", runner.runnerFuncClientWrite)
	runner.L.Register("client_close", runner.runnerFuncClientClose)

	return runner
}

// LoadScript - Load a new script into the runner
func (runner *ScriptRunner) LoadScript(script string) error {
	// TODO: Create a new fresh state

	// May need to run: eval $(luarocks path --lua-version 5.1 --bin)
	// https://github.com/luarocks/luarocks/wiki/Using-LuaRocks
	scriptErr := runner.L.DoString(script)

	if scriptErr != nil {
		println("Error loading script error:", scriptErr.Error())
	}

	return scriptErr
}

// Run - Run a global function with an event object
func (runner *ScriptRunner) Run(fnName string, eventObj interface{}) error {
	runner.Lock()
	defer runner.Unlock()

	runner.L.GetGlobal(fnName)
	runner.L.PushGoStruct(eventObj)
	scriptCallErr := runner.L.Call(1, 0)

	if scriptCallErr != nil {
		println("Script error:", scriptCallErr.Error())
	}

	return scriptCallErr
}

func (runner *ScriptRunner) runnerFuncClientWrite(L *lua.State) int {
	arg1 := L.ToString(1)
	arg2 := L.ToString(2)

	if arg1 == "" || arg2 == "" {
		return 0
	}

	c, isOk := runner.gateway.Clients.Get(arg1)
	if !isOk {
		return 0
	}

	client := c.(*Client)
	client.SendClientSignal("data", arg2)
	return 0
}

func (runner *ScriptRunner) runnerFuncClientClose(L *lua.State) int {
	clientId := L.ToString(1)
	reason := L.ToString(2)

	if clientId == "" || reason == "" {
		return 0
	}

	c, isOk := runner.gateway.Clients.Get(clientId)
	if !isOk {
		return 0
	}

	client := c.(*Client)
	client.SendClientSignal("state", "closed", reason)
	client.StartShutdown(reason)
	return 0
}
