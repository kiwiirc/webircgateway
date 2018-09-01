package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"plugin"
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
	gateway := webircgateway.NewGateway()

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

	loadPlugins(gateway)

	gateway.Start()

	justWait := make(chan bool)
	<-justWait
}

func watchForSignals(gateway *webircgateway.Gateway) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP)

	for {
		<-c
		fmt.Println("Recieved SIGHUP, reloading config file")
		gateway.Config.Load()
	}
}

func printLogOutput(gateway *webircgateway.Gateway) {
	for {
		line, _ := <-gateway.LogOutput
		log.Println(line)
	}
}

func loadPlugins(gateway *webircgateway.Gateway) {
	for _, pluginPath := range gateway.Config.Plugins {
		pluginFullPath := gateway.Config.ResolvePath(pluginPath)

		gateway.Log(2, "Loading plugin " + pluginFullPath)
		p, err := plugin.Open(pluginFullPath)
		if err != nil {
			gateway.Log(3, "Error loading plugin: " + err.Error())
			continue
		}

		startSymbol, err := p.Lookup("Start")
		if err != nil {
			gateway.Log(3, "Plugin does not export a Start function! (%s)", pluginFullPath)
			continue
		}

		startFunc := startSymbol.(func(*webircgateway.Gateway))
		startFunc(gateway)
	}
}
