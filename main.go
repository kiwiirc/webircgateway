package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/kiwiirc/webircgateway/pkg/webircgateway"
)

const VERSION = "0.2.5"

func init() {
	webircgateway.Version = VERSION
}

func main() {
	printVersion := flag.Bool("version", false, "Print the version")
	configFile := flag.String("config", "config.conf", "Config file location")
	runConfigTest := flag.Bool("test", false, "Just test the config file")
	flag.Parse()

	if *printVersion {
		fmt.Println(webircgateway.Version)
		os.Exit(0)
	}

	webircgateway.SetConfigFile(*configFile)
	log.Printf("Using config %s", webircgateway.CurrentConfigFile())

	err := webircgateway.LoadConfig()
	if err != nil {
		log.Printf("Config file error: %s", err.Error())
		os.Exit(1)
	}

	if *runConfigTest {
		// TODO: This
		log.Println("Config file is OK")
		os.Exit(0)
	}

	watchForSignals()
	webircgateway.Start()
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
