package webircgateway

import (
	"strconv"
	"sync"

	"github.com/aarzilli/golua/lua"
	"github.com/stevedonovan/luar"
)

type ScriptRunnerWorkerJob struct {
	fnName   string
	eventObj interface{}
	w        sync.WaitGroup
}

type ScriptRunnerWorker struct {
	ID      string
	L       *lua.State
	NumRuns int64
	EndChan chan struct{}
}

func NewScriptRunnerWorker(id string, queue chan *ScriptRunnerWorkerJob) *ScriptRunnerWorker {
	worker := &ScriptRunnerWorker{}
	worker.ID = id
	worker.EndChan = make(chan struct{})
	worker.L = luar.Init()
	worker.L.OpenLibs()
	go worker.Run(queue)
	return worker
}

func (worker *ScriptRunnerWorker) Run(queue chan *ScriptRunnerWorkerJob) {
	for {
		var job *ScriptRunnerWorkerJob

		select {
		case <-worker.EndChan:
			break
		case job = <-queue:
		}

		if job == nil {
			break
		}

		fnObj := luar.NewLuaObjectFromName(worker.L, job.fnName)
		scriptCallErr := fnObj.Call(nil, job.eventObj)
		fnObj.Close()

		if scriptCallErr != nil && scriptCallErr != luar.ErrLuaObjectCallable {
			println("Script error ("+job.fnName+"):", scriptCallErr.Error())
		}
		job.w.Done()
		worker.NumRuns++
	}
}

// ScriptRunner - Execute embedded Lua scripts
type ScriptRunner struct {
	gateway *Gateway
	sync.Mutex
	queue   chan *ScriptRunnerWorkerJob
	workers []*ScriptRunnerWorker
}

// NewScriptRunner - Create a new ScriptRunner
func NewScriptRunner(g *Gateway) *ScriptRunner {
	runner := &ScriptRunner{}
	runner.gateway = g
	runner.queue = make(chan *ScriptRunnerWorkerJob)
	return runner
}

func (runner *ScriptRunner) StartWorkers(numWorkers int) {
	// Tell any existing workers to stop running after their completed jobs
	for _, worker := range runner.workers {
		close(worker.EndChan)
	}

	// Now create the new workers
	for i := 0; i < numWorkers; i++ {
		workerID := "scriptworker" + strconv.Itoa(i)
		worker := NewScriptRunnerWorker(workerID, runner.queue)
		luar.Register(worker.L, "", luar.Map{
			"client_write": runner.runnerFuncClientWrite,
			"client_close": runner.runnerFuncClientClose,
			"get_client":   runner.runnerFuncGetClient,
			"print": func(args ...interface{}) {
				// Override luas default print function with ours that has more context
				var p []interface{}
				p = append(p, workerID)
				p = append(p, args...)
				runner.gateway.Log(2, "%s: %s", p...)
			},
		})

		runner.workers = append(runner.workers, worker)
	}
}

// LoadScript - Load a new script into the runner
func (runner *ScriptRunner) LoadScript(script string) error {
	// TODO: Create a new fresh state

	var lastErr error

	// May need to run: eval $(luarocks path --lua-version 5.1 --bin)
	// https://github.com/luarocks/luarocks/wiki/Using-LuaRocks
	for _, worker := range runner.workers {
		scriptErr := worker.L.DoString(script)
		if scriptErr != nil {
			runner.gateway.Log(3, "Worker %s error loading script: %s", worker.ID, scriptErr.Error())
			lastErr = scriptErr
		}
	}

	return lastErr
}

// Run - Run a global function with an event object
func (runner *ScriptRunner) Run(fnName string, eventObj interface{}) error {
	job := &ScriptRunnerWorkerJob{}
	job.eventObj = eventObj
	job.fnName = fnName

	job.w.Add(1)
	runner.queue <- job
	job.w.Wait()

	return nil
}

// AttachHooks attaches all gateway hooks into script runners
func (runner *ScriptRunner) AttachHooks() {
	HookRegister("irc.connection.pre", func(hook *HookIrcConnectionPre) {
		runner.Run("onIrcConnectionPre", hook)
	})

	HookRegister("client.state", func(hook *HookClientState) {
		runner.Run("onClientState", hook)
	})

	HookRegister("client.ready", func(hook *HookClientReady) {
		runner.Run("onClientReady", hook)
	})

	HookRegister("irc.line", func(hook *HookIrcLine) {
		runner.Run("onIrcLine", hook)
	})

	HookRegister("status.client", func(hook *HookStatus) {
		runner.Run("onStatusClient", hook)
	})

	HookRegister("gateway.closing", func(hook *HookGatewayClosing) {
		runner.Run("onGatewayCLosing", hook)
	})
}

/**
 * Functions available to the lua script
 */
func (runner *ScriptRunner) runnerFuncGetClient(L *lua.State) int {
	clientID := L.ToString(1)

	if clientID == "" {
		return 0
	}

	c, isOk := runner.gateway.Clients.Get(clientID)
	if !isOk {
		return 0
	}

	client := c.(*Client)
	luar.GoToLuaProxy(L, client)
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
	clientID := L.ToString(1)
	reason := L.ToString(2)

	if clientID == "" || reason == "" {
		return 0
	}

	c, isOk := runner.gateway.Clients.Get(clientID)
	if !isOk {
		return 0
	}

	client := c.(*Client)
	client.SendClientSignal("state", "closed", reason)
	client.StartShutdown(reason)
	return 0
}
