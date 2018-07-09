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
	// Print any webircgateway logout to STDOUT
	go printLogOutput()

	webircgateway.SetConfigFile(configFile)
	log.Printf("Using config %s", webircgateway.CurrentConfigFile())

	err := webircgateway.LoadConfig()
	if err != nil {
		log.Printf("Config file error: %s", err.Error())
		os.Exit(1)
	}

	watchForSignals()
	webircgateway.Prepare()
	webircgateway.Listen()

	justWait := make(chan bool)
	<-justWait
}

func watchForSignals() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP)
	go func() {
		for {
			<-c
			fmt.Println("Recieved SIGHUP, reloading config file")
			webircgateway.LoadConfig()
		}
	}()
}

func printLogOutput() {
	for {
		line, _ := <-webircgateway.LogOutput
		log.Println(line)
	}
}
