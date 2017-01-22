package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

func main() {
	configFile := flag.String("config", "config.conf", "Config file location")
	runConfigTest := flag.Bool("test", false, "Just test the config file")
	flag.Parse()

	Config.configFile, _ = filepath.Abs(*configFile)
	log.Printf("Using config file %s", Config.configFile)

	err := loadConfig()
	if err != nil {
		log.Printf("Config file error: %s", err.Error())
		os.Exit(1)
	}

	if *runConfigTest {
		log.Println("Config file is OK")
		os.Exit(0)
	}

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
