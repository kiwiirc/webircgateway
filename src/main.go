package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	configFile := flag.String("config", "config.conf", "Config file")
	flag.Parse()

	Config.configFile = *configFile
	log.Printf("Using config file %s", Config.configFile)
	loadConfig()

	watchForSignals()
	initListenerEngines()
	startServers()

	justWait := make(chan bool)
	<-justWait
}

func initListenerEngines() {
	engineConfigured := false
	for _, serverEngine := range Config.serverEngines {
		switch serverEngine {
		case "websocket":
			websocketHTTPHandler()
			engineConfigured = true
		case "sockjs":
			sockjsHTTPHandler()
			engineConfigured = true
		default:
			log.Printf("Invalid server engine: '%s'", serverEngine)
		}
	}

	if !engineConfigured {
		log.Fatal("No server engines configured")
	}
}

func startServers() {
	for _, server := range Config.servers {
		go startServer(server)
	}
}

func startServer(conf ConfigServer) {
	addr := fmt.Sprintf("%s:%d", conf.LocalAddr, conf.Port)

	if conf.TLS {
		//go http.ListenAndServeTLS(addr, certFile, keyFile, nil)
	} else {
		log.Printf("Listening on %s", addr)
		err := http.ListenAndServe(addr, nil)
		log.Println(err)
	}
}

func watchForSignals() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP)
	go func() {
		for {
			<-c
			fmt.Println("Recieved SIGHUP, reloading config file")
			loadConfig()
		}
	}()
}
