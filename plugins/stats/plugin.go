package main

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/kiwiirc/webircgateway/pkg/webircgateway"
)

func Start(gateway *webircgateway.Gateway, pluginsQuit *sync.WaitGroup) {
	gateway.Log(2, "Stats reporting plugin loading")
	go reportUsage(gateway)

	pluginsQuit.Done()
}

func reportUsage(gateway *webircgateway.Gateway) {
	started := time.Now()

	out := func(line string) {
		file, _ := os.OpenFile("stats_"+fmt.Sprintf("%v", started.Unix())+".csv",
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		file.WriteString(line)
		file.Close()
	}

	out("time,rss,heapinuse,heapalloc,numroutines,numclients\n")

	for {
		time.Sleep(time.Second * 5)

		numClients := gateway.Clients.Count()
		mem := &runtime.MemStats{}
		runtime.ReadMemStats(mem)

		line := fmt.Sprintf(
			"%v,%v,%v,%v,%v,%v\n",
			math.Round(time.Now().Sub(started).Seconds()),
			mem.Sys/1024,
			mem.HeapInuse/1024,
			mem.HeapAlloc/1024,
			runtime.NumGoroutine(),
			numClients,
		)

		out(line)
	}
}
