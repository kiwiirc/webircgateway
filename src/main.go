package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"webircgateway/identd"

	"rsc.io/letsencrypt"
)

var (
	// Version - The current version of webircgateway
	Version    = "0.2.3"
	identdServ identd.Server
)

func main() {
	printVersion := flag.Bool("version", false, "Print the version")
	configFile := flag.String("config", "config.conf", "Config file location")
	runConfigTest := flag.Bool("test", false, "Just test the config file")
	flag.Parse()

	if *printVersion {
		fmt.Println(Version)
		os.Exit(0)
	}

	SetConfigFile(*configFile)
	log.Printf("Using config %s", Config.configFile)

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
	// Add some general server info about this webircgateway instance
	http.HandleFunc("/webirc/", func(w http.ResponseWriter, r *http.Request) {
		out, _ := json.Marshal(map[string]interface{}{
			"name":    "webircgateway",
			"version": Version,
		})

		w.Write(out)
	})

	for _, server := range Config.servers {
		go startServer(server)
	}
}

func startServer(conf ConfigServer) {
	addr := fmt.Sprintf("%s:%d", conf.LocalAddr, conf.Port)

	if conf.TLS && conf.LetsEncryptCacheFile == "" {
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
	} else if conf.TLS && conf.LetsEncryptCacheFile != "" {
		m := letsencrypt.Manager{}
		err := m.CacheFile(conf.LetsEncryptCacheFile)
		if err != nil {
			log.Printf("Failed to listen with letsencrypt TLS: %s", err.Error())
		}
		log.Printf("Listening with letsencrypt TLS on %s", addr)
		srv := &http.Server{
			Addr: addr,
			TLSConfig: &tls.Config{
				GetCertificate: m.GetCertificate,
			},
		}
		err = srv.ListenAndServeTLS("", "")
		log.Printf("Listening with letsencrypt failed: %s", err.Error())
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
