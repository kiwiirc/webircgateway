package webircgateway

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"errors"

	"github.com/kiwiirc/webircgateway/pkg/identd"
	"golang.org/x/crypto/acme/autocert"
)

var (
	// Version - The current version of webircgateway
	Version     = "-"
	identdServ  identd.Server
	HttpRouter  *http.ServeMux
	LogOutput   chan string
	messageTags *MessageTagManager

	// ensure only one instance of the manager and handler is running
	// while allowing multiple listeners to use it
	letsencryptMutex sync.Mutex
	letsencryptManager *autocert.Manager
)

func init() {
	HttpRouter = http.NewServeMux()
	LogOutput = make(chan string, 5)
	messageTags = NewMessageTagManager()
}

func Prepare() {
	maybeStartStaticFileServer()
	initHttpRoutes()
	maybeStartIdentd()
}

func maybeStartIdentd() {
	identdServ = identd.NewIdentdServer()

	if Config.Identd {
		err := identdServ.Run()
		if err != nil {
			logOut(3, "Error starting identd server: %s", err.Error())
		} else {
			logOut(2, "Identd server started")
		}
	}
}

func maybeStartStaticFileServer() {
	if Config.Webroot != "" {
		webroot := ConfigResolvePath(Config.Webroot)
		logOut(2, "Serving files from %s", webroot)
		HttpRouter.Handle("/", http.FileServer(http.Dir(webroot)))
	}
}

func initHttpRoutes() error {
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
			logOut(3, "Invalid server engine: '%s'", serverEngine)
		}
	}

	if !engineConfigured {
		logOut(3, "No server engines configured")
		return errors.New("No server engines configured")
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
		for item := range Clients.Iter() {
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

	return nil
}

func Listen() {
	for _, server := range Config.Servers {
		go startServer(server)
	}
}

func logOut(level int, format string, args ...interface{}) {
	if level < Config.LogLevel {
		return
	}

	levels := [...]string{"L_DEBUG", "L_INFO", "L_WARN"}
	line := fmt.Sprintf(levels[level-1]+" "+format, args...)

	select {
	case LogOutput <- line:
	}
}

func startServer(conf ConfigServer) {
	addr := fmt.Sprintf("%s:%d", conf.LocalAddr, conf.Port)

	if strings.HasPrefix(strings.ToLower(conf.LocalAddr), "tcp:") {
		tcpStartHandler(conf.LocalAddr[4:] + ":" + strconv.Itoa(conf.Port))
	} else if conf.TLS && conf.LetsEncryptCacheDir == "" {
		if conf.CertFile == "" || conf.KeyFile == "" {
			logOut(3, "'cert' and 'key' options must be set for TLS servers")
			return
		}

		tlsCert := ConfigResolvePath(conf.CertFile)
		tlsKey := ConfigResolvePath(conf.KeyFile)

		logOut(2, "Listening with TLS on %s", addr)
		keyPair, keyPairErr := tls.LoadX509KeyPair(tlsCert, tlsKey)
		if keyPairErr != nil {
			logOut(3, "Failed to listen with TLS, certificate error: %s", keyPairErr.Error())
			return
		}
		srv := &http.Server{
			Addr: addr,
			TLSConfig: &tls.Config{
				Certificates: []tls.Certificate{keyPair},
			},
			Handler: HttpRouter,
		}

		// Don't use HTTP2 since it doesn't support websockets
		srv.TLSNextProto = make(map[string]func(*http.Server, *tls.Conn, http.Handler))

		err := srv.ListenAndServeTLS("", "")
		if err != nil {
			logOut(3, "Failed to listen with TLS: %s", err.Error())
		}
	} else if conf.TLS && conf.LetsEncryptCacheDir != "" {
		letsencryptMutex.Lock()
		if letsencryptManager == nil {
			letsencryptManager := &autocert.Manager{
				Prompt:     autocert.AcceptTOS,
				Cache:      autocert.DirCache(conf.LetsEncryptCacheDir + "/"),
			}
			HttpRouter.Handle("/.well-known/", letsencryptManager.HTTPHandler(HttpRouter))
		}
		letsencryptMutex.Unlock()

		logOut(2, "Listening with letsencrypt TLS on %s", addr)
		srv := &http.Server{
			Addr: addr,
			TLSConfig: &tls.Config{
				GetCertificate: letsencryptManager.GetCertificate,
			},
			Handler: HttpRouter,
		}

		// Don't use HTTP2 since it doesn't support websockets
		srv.TLSNextProto = make(map[string]func(*http.Server, *tls.Conn, http.Handler))

		err := srv.ListenAndServeTLS("", "")
		logOut(3, "Listening with letsencrypt failed: %s", err.Error())
	} else if strings.HasPrefix(strings.ToLower(conf.LocalAddr), "unix:") {
		socketFile := conf.LocalAddr[5:]
		logOut(2, "Listening on %s", socketFile)
		os.Remove(socketFile)
		server, serverErr := net.Listen("unix", socketFile)
		if serverErr != nil {
			logOut(3, serverErr.Error())
			return
		}
		os.Chmod(socketFile, conf.BindMode)
		http.Serve(server, HttpRouter)
	} else {
		logOut(2, "Listening on %s", addr)
		err := http.ListenAndServe(addr, HttpRouter)
		logOut(3, err.Error())
	}
}
