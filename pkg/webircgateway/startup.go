package webircgateway

import (
	"context"
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
	cmap "github.com/orcaman/concurrent-map"
	"golang.org/x/crypto/acme/autocert"
)

type Server struct {
	Config      *Config
	HttpRouter  *http.ServeMux
	LogOutput   chan string
	messageTags *MessageTagManager
	identdServ  identd.Server
	Clients     cmap.ConcurrentMap
}

func NewServer() *Server {
	s := &Server{}
	s.HttpRouter = http.NewServeMux()
	s.LogOutput = make(chan string, 5)
	s.identdServ = identd.NewIdentdServer()
	s.messageTags = NewMessageTagManager()
	// Clients hold a map lookup for all the connected clients
	s.Clients = cmap.New()

	return s
}

func (s *Server) Log(level int, format string, args ...interface{}) {
	if level < Config.LogLevel {
		return
	}

	levels := [...]string{"L_DEBUG", "L_INFO", "L_WARN"}
	line := fmt.Sprintf(levels[level-1]+" "+format, args...)

	select {
	case s.LogOutput <- line:
	}
}

func (s *Server) Start() {
	s.maybeStartStaticFileServer()
	s.initHttpRoutes()
	s.maybeStartIdentd()

	for _, server := range Config.Servers {
		go startServer(server)
	}
}

func (s *Server) maybeStartStaticFileServer() {
	if Config.Webroot != "" {
		webroot := ConfigResolvePath(Config.Webroot)
		s.Log(2, "Serving files from %s", webroot)
		s.HttpRouter.Handle("/", http.FileServer(http.Dir(webroot)))
	}
}

func (s *Server) initHttpRoutes() error {
	// Add all the transport routes
	engineConfigured := false
	for _, serverEngine := range Config.ServerEngines {
		switch serverEngine {
		case "kiwiirc":
			kiwiircHTTPHandler(s.HttpRouter)
			engineConfigured = true
		case "websocket":
			websocketHTTPHandler(s.HttpRouter)
			engineConfigured = true
		case "sockjs":
			sockjsHTTPHandler(s.HttpRouter)
			engineConfigured = true
		default:
			s.Log(3, "Invalid server engine: '%s'", serverEngine)
		}
	}

	if !engineConfigured {
		s.Log(3, "No server engines configured")
		return errors.New("No server engines configured")
	}

	// Add some general server info about this webircgateway instance
	s.HttpRouter.HandleFunc("/webirc/", func(w http.ResponseWriter, r *http.Request) {
		out, _ := json.Marshal(map[string]interface{}{
			"name":    "webircgateway",
			"version": Version,
		})

		w.Write(out)
	})

	s.HttpRouter.HandleFunc("/webirc/_status", func(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) maybeStartIdentd() {
	if Config.Identd {
		err := s.identdServ.Run()
		if err != nil {
			s.Log(3, "Error starting identd server: %s", err.Error())
		} else {
			s.Log(2, "Identd server started")
		}
	}
}

func (s *Server) startServer(conf ConfigServer) {
	addr := fmt.Sprintf("%s:%d", conf.LocalAddr, conf.Port)

	if strings.HasPrefix(strings.ToLower(conf.LocalAddr), "tcp:") {
		tcpStartHandler(conf.LocalAddr[4:] + ":" + strconv.Itoa(conf.Port))
	} else if conf.TLS && conf.LetsEncryptCacheDir == "" {
		if conf.CertFile == "" || conf.KeyFile == "" {
			s.Log(3, "'cert' and 'key' options must be set for TLS servers")
			return
		}

		tlsCert := ConfigResolvePath(conf.CertFile)
		tlsKey := ConfigResolvePath(conf.KeyFile)

		s.Log(2, "Listening with TLS on %s", addr)
		keyPair, keyPairErr := tls.LoadX509KeyPair(tlsCert, tlsKey)
		if keyPairErr != nil {
			s.Log(3, "Failed to listen with TLS, certificate error: %s", keyPairErr.Error())
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
			s.Log(3, "Failed to listen with TLS: %s", err.Error())
		}
	} else if conf.TLS && conf.LetsEncryptCacheDir != "" {
		s.Log(2, "Listening with letsencrypt TLS on %s", addr)
		leManager := getLEManager(conf.LetsEncryptCacheDir)
		srv := &http.Server{
			Addr: addr,
			TLSConfig: &tls.Config{
				GetCertificate: leManager.GetCertificate,
			},
			Handler: HttpRouter,
		}

		// Don't use HTTP2 since it doesn't support websockets
		srv.TLSNextProto = make(map[string]func(*http.Server, *tls.Conn, http.Handler))

		err := srv.ListenAndServeTLS("", "")
		s.Log(3, "Listening with letsencrypt failed: %s", err.Error())
	} else if strings.HasPrefix(strings.ToLower(conf.LocalAddr), "unix:") {
		socketFile := conf.LocalAddr[5:]
		s.Log(2, "Listening on %s", socketFile)
		os.Remove(socketFile)
		server, serverErr := net.Listen("unix", socketFile)
		if serverErr != nil {
			s.Log(3, serverErr.Error())
			return
		}
		os.Chmod(socketFile, conf.BindMode)
		http.Serve(server, HttpRouter)
	} else {
		s.Log(2, "Listening on %s", addr)
		err := http.ListenAndServe(addr, HttpRouter)
		s.Log(3, err.Error())
	}
}

var (
	// Version - The current version of webircgateway
	Version = "-"
	//identdServ  identd.Server
	//HttpRouter  *http.ServeMux
	//LogOutput   chan string
	//messageTags *MessageTagManager

	// ensure only one instance of the manager and handler is running
	// while allowing multiple listeners to use it
	letsencryptMutex   sync.Mutex
	letsencryptManager *autocert.Manager
)

func getLEManager(certCacheDir string) *autocert.Manager {
	letsencryptMutex.Lock()
	defer letsencryptMutex.Unlock()

	// Create it if it doesn't already exist
	if letsencryptManager == nil {
		letsencryptManager = &autocert.Manager{
			Prompt: autocert.AcceptTOS,
			Cache:  autocert.DirCache(strings.TrimRight(certCacheDir, "/")),
			HostPolicy: func(ctx context.Context, host string) error {
				s.Log(2, "Automatically requesting a HTTPS certificate for %s", host)
				return nil
			},
		}
		HttpRouter.Handle("/.well-known/", letsencryptManager.HTTPHandler(HttpRouter))
	}

	return letsencryptManager
}
