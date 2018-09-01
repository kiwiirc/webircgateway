package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/kiwiirc/webircgateway/pkg/proxy"
	"github.com/kiwiirc/webircgateway/pkg/webircgateway"
)

const VERSION = "0.5.0"

func init() {
	webircgateway.Version = VERSION
}

func main() {
	printVersion := flag.Bool("version", false, "Print the version")
	configFile := flag.String("config", "config.conf", "Config file location")
	startSection := flag.String("run", "gateway", "What type of server to run")
	flag.Parse()

	if *printVersion {
		fmt.Println(webircgateway.Version)
		os.Exit(0)
	}

	switch *startSection {
	case "proxy":
		runProxy()
	case "gateway":
		runGateway(*configFile)
	}
}

func runProxy() {
	proxy.Start(os.Getenv("listen"))
}

func runGateway(configFile string) {
	gateway := webircgateway.NewServer()

	// Print any webircgateway logout to STDOUT
	go printLogOutput(gateway)

	// Listen for process signals
	go watchForSignals(gateway)

	gateway.Config.SetConfigFile(configFile)
	log.Printf("Using config %s", gateway.Config.CurrentConfigFile())

	configErr := gateway.Config.Load()
	if configErr != nil {
		log.Printf("Config file error: %s", configErr.Error())
		os.Exit(1)
	}

	gateway.Start()

	justWait := make(chan bool)
	<-justWait
}

func watchForSignals(gateway *webircgateway.Server) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP)

	for {
		<-c
		fmt.Println("Recieved SIGHUP, reloading config file")
		gateway.Config.Load()
	}
}

func printLogOutput(gateway *webircgateway.Server) {
	for {
		line, _ := <-gateway.LogOutput
		log.Println(line)
	}
}
