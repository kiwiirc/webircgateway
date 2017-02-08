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
	"webircgateway/identd"
)

var identdServ identd.Server

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
	maybeStartStaticFileServer()
	initListenerEngines()
	startServers()
	maybeStartIdentd()

	justWait := make(chan bool)
	<-justWait
}

func initListenerEngines() {
	engineConfigured := false
	for _, serverEngine := range Config.serverEngines {
		switch serverEngine {
		case "kiwiirc":
			kiwiircHTTPHandler()
			engineConfigured = true
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

func maybeStartIdentd() {
	identdServ = identd.NewIdentdServer()

	if Config.identd {
		err := identdServ.Run()
		if err != nil {
			log.Printf("Error starting identd server: %s", err.Error())
		} else {
			log.Printf("Identd server started")
		}
	}
}

func maybeStartStaticFileServer() {
	if Config.webroot != "" {
		webroot := ConfigResolvePath(Config.webroot)
		log.Printf("Serving files from %s", webroot)
		http.Handle("/", http.FileServer(http.Dir(webroot)))
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
		if conf.CertFile == "" || conf.KeyFile == "" {
			log.Println("'cert' and 'key' options must be set for TLS servers")
			return
		}

		tlsCert := ConfigResolvePath(conf.CertFile)
		tlsKey := ConfigResolvePath(conf.KeyFile)

		log.Printf("Listening with TLS on %s", addr)
		err := http.ListenAndServeTLS(addr, tlsCert, tlsKey, nil)
		if err != nil {
			log.Printf("Failed to listen with TLS: %s", err.Error())
		}
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
