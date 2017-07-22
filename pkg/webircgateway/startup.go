package webircgateway

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/kiwiirc/webircgateway/pkg/identd"
	"rsc.io/letsencrypt"
)

var (
	// Version - The current version of webircgateway
	Version    = "-"
	identdServ identd.Server
	HttpRouter *http.ServeMux
)

func Init() {
	HttpRouter = http.NewServeMux()

	maybeStartStaticFileServer()
	initHttpRoutes()
	maybeStartIdentd()
}

func maybeStartIdentd() {
	identdServ = identd.NewIdentdServer()

	if Config.Identd {
		err := identdServ.Run()
		if err != nil {
			log.Printf("Error starting identd server: %s", err.Error())
		} else {
			log.Printf("Identd server started")
		}
	}
}

func maybeStartStaticFileServer() {
	if Config.Webroot != "" {
		webroot := ConfigResolvePath(Config.Webroot)
		log.Printf("Serving files from %s", webroot)
		http.Handle("/", http.FileServer(http.Dir(webroot)))
	}
}

func initHttpRoutes() {
	// Add all the transport routes
	engineConfigured := false
	for _, serverEngine := range Config.ServerEngines {
		switch serverEngine {
		case "kiwiirc":
			kiwiircHTTPHandler(HttpRouter)
			engineConfigured = true
		case "websocket":
			websocketHTTPHandler(HttpRouter)
			engineConfigured = true
		case "sockjs":
			sockjsHTTPHandler(HttpRouter)
			engineConfigured = true
		default:
			log.Printf("Invalid server engine: '%s'", serverEngine)
		}
	}

	if !engineConfigured {
		log.Fatal("No server engines configured")
	}

	// Add some general server info about this webircgateway instance
	HttpRouter.HandleFunc("/webirc/", func(w http.ResponseWriter, r *http.Request) {
		out, _ := json.Marshal(map[string]interface{}{
			"name":    "webircgateway",
			"version": Version,
		})

		w.Write(out)
	})

	HttpRouter.HandleFunc("/webirc/_status", func(w http.ResponseWriter, r *http.Request) {
		if GetRemoteAddressFromRequest(r).String() != "127.0.0.1" {
			w.WriteHeader(403)
			return
		}

		out := ""
		for item := range clients.Iter() {
			c := item.Val.(*Client)
			out += fmt.Sprintf(
				"%s %s %s %s!%s\n",
				c.RemoteAddr,
				c.RemoteHostname,
				c.State,
				c.IrcState.Nick,
				c.IrcState.Username,
			)
		}

		w.Write([]byte(out))
	})
}

func Listen() {
	for _, server := range Config.Servers {
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
		err := http.ListenAndServeTLS(addr, tlsCert, tlsKey, HttpRouter)
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
			Handler: HttpRouter,
		}
		err = srv.ListenAndServeTLS("", "")
		log.Printf("Listening with letsencrypt failed: %s", err.Error())
	} else {
		log.Printf("Listening on %s", addr)
		err := http.ListenAndServe(addr, HttpRouter)
		log.Println(err)
	}
}
