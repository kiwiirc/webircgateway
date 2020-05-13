package webircgateway

import (
	"strconv"
	"sync"

	"github.com/aarzilli/golua/lua"
	"github.com/stevedonovan/luar"
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
	runner.L = luar.Init()
	runner.L.OpenLibs()

	luar.Register(runner.L, "", luar.Map{
		"client_write": runner.runnerFuncClientWrite,
		"client_close": runner.runnerFuncClientClose,
		"get_client":   runner.runnerFuncGetClient,
	})

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

	f := luar.NewLuaObjectFromName(runner.L, fnName)
	scriptCallErr := f.Call(nil, eventObj)
	f.Close()

	// ErrLuaObjectCallable = "LuaObject must be callable" - The function doesn't exist
	if scriptCallErr != nil && scriptCallErr != luar.ErrLuaObjectCallable {
		println("Script error ("+fnName+"):", scriptCallErr.Error())
	}

	return scriptCallErr
}

func (runner *ScriptRunner) AttachHooks() {
	HookRegister("irc.connection.pre", func(hook *HookIrcConnectionPre) {
		eventObj := &struct {
			ClientId string
			Upstream *ConfigUpstream
		}{
			ClientId: strconv.FormatUint(hook.Client.Id, 10),
			Upstream: hook.UpstreamConfig,
		}

		runner.Run("onIrcConnectionPre", eventObj)
	})

	HookRegister("client.state", func(hook *HookClientState) {
		eventObj := &struct {
			ClientId  string
			Connected bool
		}{
			ClientId:  strconv.FormatUint(hook.Client.Id, 10),
			Connected: hook.Connected,
		}

		runner.Run("onClientState", eventObj)
	})

	HookRegister("client.ready", func(hook *HookClientReady) {
		eventObj := &struct {
			ClientId string
		}{
			ClientId: strconv.FormatUint(hook.Client.Id, 10),
		}

		runner.Run("onClientReady", eventObj)
	})

	HookRegister("irc.line", func(hook *HookIrcLine) {
		runner.Run("onIrcLine", hook)
	})
}

func (runner *ScriptRunner) runnerFuncGetClient(L *lua.State) int {
	clientId := L.ToString(1)

	if clientId == "" {
		return 0
	}

	c, isOk := runner.gateway.Clients.Get(clientId)
	if !isOk {
		return 0
	}

	client := c.(*Client)
	luar.GoToLuaProxy(runner.L, client)
	return 1
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
