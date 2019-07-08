package webircgateway

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/time/rate"

	"sync"

	"github.com/kiwiirc/webircgateway/pkg/irc"
	"github.com/kiwiirc/webircgateway/pkg/proxy"
)

const (
	// ClientStateIdle - Client connected and just sat there
	ClientStateIdle = "idle"
	// ClientStateConnecting - Connecting upstream
	ClientStateConnecting = "connecting"
	// ClientStateRegistering - Registering to the IRC network
	ClientStateRegistering = "registering"
	// ClientStateConnected - Connected upstream
	ClientStateConnected = "connected"
	// ClientStateEnding - Client is ending its connection
	ClientStateEnding = "ending"
)

type ClientSignal [3]string

// Client - Connecting client struct
type Client struct {
	Gateway          *Gateway
	Id               uint64
	State            string
	shuttingDownLock sync.Mutex
	shuttingDown     bool
	ShutdownReason   string
	SeenQuit         bool
	Recv             chan string
	ThrottledRecv    *ThrottledStringChannel
	UpstreamSendIn   chan string
	UpstreamSendOut  chan string
	upstream         io.ReadWriteCloser
	UpstreamRecv     chan string
	UpstreamStarted  bool
	UpstreamConfig   *ConfigUpstream
	DestHost         string
	DestPort         int
	DestTLS          bool
	IrcState         *irc.State
	Encoding         string
	// Captchas may be needed to verify a client
	Verified bool
	SentPass bool
	// Signals for the transport to make use of (data, connection state, etc)
	Signals  chan ClientSignal
	Features struct {
		Messagetags bool
		Metadata    bool
		ExtJwt      bool
	}
	// The specific message-tags CAP that the client has requested if we are wrapping it
	RequestedMessageTagsCap string
	*ClientConnectionInfo
}

var nextClientID uint64 = 1

// ClientConnectionInfo contains information about a client connection. It is a
// separate struct so that it can be populated before calling NewClient, so that
// this info can be conveniently accessed even when NewClient does not return a
// *Client value (returns an error)
type ClientConnectionInfo struct {
	Origin string
	// Tags get passed upstream via the WEBIRC command
	Tags           map[string]string
	RemoteAddr     string
	RemoteHostname string
	Request        *http.Request
}

func NewClientConnectionInfo(
	origin string,
	remoteAddr string,
	req *http.Request,
	gateway *Gateway,
) *ClientConnectionInfo {
	connInfo := &ClientConnectionInfo{
		Origin: origin,

		Tags:       make(map[string]string),
		RemoteAddr: remoteAddr,
		Request:    req,
	}

	connInfo.ResolveHostname()

	if req != nil && gateway.isRequestSecure(req) {
		connInfo.Tags["secure"] = ""
	}

	// This doesn't make sense to have since the remote port may change between requests. Only
	// here for testing purposes for now.
	_, remoteAddrPort, _ := net.SplitHostPort(connInfo.RemoteAddr)
	connInfo.Tags["remote-port"] = remoteAddrPort

	return connInfo
}

func (connInfo *ClientConnectionInfo) ResolveHostname() {
	clientHostnames, err := net.LookupAddr(connInfo.RemoteAddr)
	if err != nil || len(clientHostnames) == 0 {
		connInfo.RemoteHostname = connInfo.RemoteAddr
	} else {
		// FQDNs include a . at the end. Strip it out
		potentialHostname := strings.Trim(clientHostnames[0], ".")

		// Must check that the resolved hostname also resolves back to the users IP
		addr, err := net.LookupIP(potentialHostname)
		if err == nil && len(addr) == 1 && addr[0].String() == connInfo.RemoteAddr {
			connInfo.RemoteHostname = potentialHostname
		} else {
			connInfo.RemoteHostname = connInfo.RemoteAddr
		}
	}
}

func LogNewClientError(gateway *Gateway, info *ClientConnectionInfo, err error) {
	hook := HookNewClientError{
		Gateway:              gateway,
		ClientConnectionInfo: info,
		Error:                err,
	}

	hook.Dispatch("new.client.error")
}

// NewClient - Makes a new client
func NewClient(gateway *Gateway, info *ClientConnectionInfo) (*Client, error) {
	if !gateway.isClientOriginAllowed(info.Origin) {

		err := fmt.Errorf("Origin %s not allowed. Closing connection", info.Origin)
		LogNewClientError(gateway, info, err)
		return nil, err
	}

	thisID := atomic.AddUint64(&nextClientID, 1)

	recv := make(chan string, 50)
	c := &Client{
		Gateway:              gateway,
		Id:                   thisID,
		State:                ClientStateIdle,
		Recv:                 recv,
		ThrottledRecv:        NewThrottledStringChannel(recv, rate.NewLimiter(rate.Inf, 1)),
		UpstreamSendIn:       make(chan string, 50),
		UpstreamSendOut:      make(chan string, 50),
		UpstreamRecv:         make(chan string, 50),
		Encoding:             "UTF-8",
		Signals:              make(chan ClientSignal, 50),
		IrcState:             irc.NewState(),
		UpstreamConfig:       &ConfigUpstream{},
		ClientConnectionInfo: info,
	}

	// Auto enable some features by default. They may be disabled later on
	c.Features.ExtJwt = true

	// Auto verify the client if it's not needed
	if !gateway.Config.RequiresVerification {
		c.Verified = true
	}

	go c.clientLineWorker()

	// Add to the clients maps and wait until everything has been marked
	// as completed (several routines add themselves to EndWG so that we can catch
	// when they are all completed)
	gateway.Clients.Set(string(c.Id), c)

	// trigger initial client.state hook
	c.setState(ClientStateIdle)

	return c, nil
}

// Log - Log a line of text with context of this client
func (c *Client) Log(level int, format string, args ...interface{}) {
	prefix := fmt.Sprintf("client:%d ", c.Id)
	c.Gateway.LogWithoutHook(level, prefix+format, args...)

	hook := &HookLog{
		Client: c,
		Level:  level,
		Line:   fmt.Sprintf(format, args...),
	}
	hook.Dispatch("client.log")
}

func (c *Client) IsShuttingDown() bool {
	c.shuttingDownLock.Lock()
	defer c.shuttingDownLock.Unlock()
	return c.shuttingDown
}

func (c *Client) StartShutdown(reason string) {
	c.shuttingDownLock.Lock()
	defer c.shuttingDownLock.Unlock()

	c.Log(1, "StartShutdown(%s) ShuttingDown=%t", reason, c.shuttingDown)
	if !c.shuttingDown {
		lastState := c.State
		c.shuttingDown = true
		c.ShutdownReason = reason
		c.setState(ClientStateEnding)

		switch reason {
		case "upstream_closed":
			c.Log(2, "Upstream closed the connection")
		case "err_connecting_upstream":
		case "err_no_upstream":
			// Error has been logged already
		case "client_closed":
			if !c.SeenQuit && c.Gateway.Config.SendQuitOnClientClose != "" && lastState == ClientStateConnected {
				c.processLineToUpstream("QUIT :" + c.Gateway.Config.SendQuitOnClientClose)
			}
			c.Log(2, "Client disconnected")
		default:
			c.Log(2, "Closed: %s", reason)
		}

		close(c.Signals)
		c.Gateway.Clients.Remove(string(c.Id))
	}
}

func (c *Client) SendClientSignal(signal string, args ...string) {
	c.shuttingDownLock.Lock()
	defer c.shuttingDownLock.Unlock()

	if !c.shuttingDown {
		switch len(args) {
		case 0:
			c.Signals <- ClientSignal{signal}
		case 1:
			c.Signals <- ClientSignal{signal, args[0]}
		case 2:
			c.Signals <- ClientSignal{signal, args[0], args[1]}
		}
	}
}

func (c *Client) SendIrcError(message string) {
	c.SendClientSignal("data", "ERROR :"+message)
}

func (c *Client) connectUpstream() {
	client := c

	c.UpstreamStarted = true

	var upstreamConfig ConfigUpstream

	if client.DestHost == "" {
		client.Log(2, "Using configured upstream")
		var err error
		upstreamConfig, err = c.Gateway.findUpstream()
		if err != nil {
			client.Log(3, "No upstreams available")
			client.SendIrcError("The server has not been configured")
			client.StartShutdown("err_no_upstream")
			return
		}
	} else {
		if !c.Gateway.isIrcAddressAllowed(client.DestHost) {
			client.Log(2, "Server %s is not allowed. Closing connection", client.DestHost)
			client.SendIrcError("Not allowed to connect to " + client.DestHost)
			client.SendClientSignal("state", "closed", "err_forbidden")
			client.StartShutdown("err_no_upstream")
			return
		}

		client.Log(2, "Using client given upstream")
		upstreamConfig = c.configureUpstream()
	}

	c.UpstreamConfig = &upstreamConfig

	hook := &HookIrcConnectionPre{
		Client:         client,
		UpstreamConfig: &upstreamConfig,
	}
	hook.Dispatch("irc.connection.pre")
	if hook.Halt {
		client.SendClientSignal("state", "closed", "err_forbidden")
		client.StartShutdown("err_connecting_upstream")
		return
	}

	client.setState(ClientStateConnecting)

	upstream, upstreamErr := client.makeUpstreamConnection()
	if upstreamErr != nil {
		// Error handling was already managed in makeUpstreamConnection()
		return
	}

	client.setState(ClientStateRegistering)

	go func() {
		for {
			line, ok := <-client.UpstreamSendIn
			if !ok {
				return
			}
			client.UpstreamSendOut <- line
		}
	}()

	client.writeWebircLines(upstream)
	client.maybeSendPass(upstream)
	client.SendClientSignal("state", "connected")
	client.proxyData(upstream)
	client.upstream = upstream
}

func (c *Client) makeUpstreamConnection() (io.ReadWriteCloser, error) {
	client := c
	upstreamConfig := c.UpstreamConfig

	var connection io.ReadWriteCloser

	if upstreamConfig.Proxy == nil {
		// Connect directly to the IRCd
		dialer := net.Dialer{}
		dialer.Timeout = time.Second * time.Duration(upstreamConfig.Timeout)

		var conn net.Conn
		var connErr error
		if upstreamConfig.Network == "unix" {
			conn, connErr = dialer.Dial("unix", upstreamConfig.Hostname)
		} else {
			upstreamStr := fmt.Sprintf("%s:%d", upstreamConfig.Hostname, upstreamConfig.Port)
			conn, connErr = dialer.Dial("tcp", upstreamStr)
		}

		if connErr != nil {
			client.Log(3, "Error connecting to the upstream IRCd. %s", connErr.Error())
			errString := ""
			if errString = typeOfErr(connErr); errString != "" {
				errString = "err_" + errString
			}
			client.SendClientSignal("state", "closed", errString)
			client.StartShutdown("err_connecting_upstream")
			return nil, errors.New("error connecting upstream")
		}

		// Add the ports into the identd before possible TLS handshaking. If we do it after then
		// there's a good chance the identd lookup will occur before the handshake has finished
		if c.Gateway.Config.Identd {
			// Keep track of the upstreams local and remote port numbers
			_, lPortStr, _ := net.SplitHostPort(conn.LocalAddr().String())
			client.IrcState.LocalPort, _ = strconv.Atoi(lPortStr)
			_, rPortStr, _ := net.SplitHostPort(conn.RemoteAddr().String())
			client.IrcState.RemotePort, _ = strconv.Atoi(rPortStr)

			c.Gateway.identdServ.AddIdent(client.IrcState.LocalPort, client.IrcState.RemotePort, client.IrcState.Username, "")
		}

		if upstreamConfig.TLS {
			tlsConfig := &tls.Config{InsecureSkipVerify: true}
			tlsConn := tls.Client(conn, tlsConfig)
			err := tlsConn.Handshake()
			if err != nil {
				client.Log(3, "Error connecting to the upstream IRCd. %s", err.Error())
				client.SendClientSignal("state", "closed", "err_tls")
				client.StartShutdown("err_connecting_upstream")
				return nil, errors.New("error connecting upstream")
			}

			conn = net.Conn(tlsConn)
		}

		connection = conn
	}

	if upstreamConfig.Proxy != nil {
		// Connect to the IRCd via a proxy
		conn := proxy.MakeKiwiProxyConnection()
		conn.DestHost = upstreamConfig.Hostname
		conn.DestPort = upstreamConfig.Port
		conn.DestTLS = upstreamConfig.TLS
		conn.Username = upstreamConfig.Proxy.Username
		conn.ProxyInterface = upstreamConfig.Proxy.Interface

		dialErr := conn.Dial(fmt.Sprintf(
			"%s:%d",
			upstreamConfig.Proxy.Hostname,
			upstreamConfig.Proxy.Port,
		))

		if dialErr != nil {
			errString := ""
			if errString = typeOfErr(dialErr); errString != "" {
				errString = "err_" + errString
			} else {
				errString = "err_proxy"
			}
			client.Log(3,
				"Error connecting to the kiwi proxy, %s:%d. %s",
				upstreamConfig.Proxy.Hostname,
				upstreamConfig.Proxy.Port,
				dialErr.Error(),
			)

			client.SendClientSignal("state", "closed", errString)
			client.StartShutdown("err_connecting_upstream")
			return nil, errors.New("error connecting upstream")
		}

		connection = conn
	}

	return connection, nil
}

func (c *Client) writeWebircLines(upstream io.ReadWriteCloser) {
	// Send any WEBIRC lines
	if c.UpstreamConfig.WebircPassword == "" {
		c.Log(1, "No webirc to send")
		return
	}

	gatewayName := "webircgateway"
	if c.Gateway.Config.GatewayName != "" {
		gatewayName = c.Gateway.Config.GatewayName
	}
	if c.UpstreamConfig.GatewayName != "" {
		gatewayName = c.UpstreamConfig.GatewayName
	}

	webircTags := c.buildWebircTags()
	if strings.Contains(webircTags, " ") {
		webircTags = ":" + webircTags
	}

	clientHostname := c.RemoteHostname
	if c.Gateway.Config.ClientHostname != "" {
		clientHostname = makeClientReplacements(c.Gateway.Config.ClientHostname, c)
	}

	remoteAddr := c.RemoteAddr
	// Prefix IPv6 addresses that start with a : so they can be sent as an individual IRC
	//  parameter. eg. ::1 would not parse correctly as a parameter, while 0::1 will
	if strings.HasPrefix(remoteAddr, ":") {
		remoteAddr = "0" + remoteAddr
	}

	webircLine := fmt.Sprintf(
		"WEBIRC %s %s %s %s %s\n",
		c.UpstreamConfig.WebircPassword,
		gatewayName,
		clientHostname,
		remoteAddr,
		webircTags,
	)
	c.Log(1, "->upstream: %s", webircLine)
	upstream.Write([]byte(webircLine))
}

func (c *Client) maybeSendPass(upstream io.ReadWriteCloser) {
	if c.UpstreamConfig.ServerPassword == "" {
		return
	}
	c.SentPass = true
	passLine := fmt.Sprintf(
		"PASS %s\n",
		c.UpstreamConfig.ServerPassword,
	)
	c.Log(1, "->upstream: %s", passLine)
	upstream.Write([]byte(passLine))
}

func (c *Client) proxyData(upstream io.ReadWriteCloser) {
	client := c

	// Data from upstream to client
	go func() {
		reader := bufio.NewReader(upstream)
		for {
			data, err := reader.ReadString('\n')
			if err != nil {
				break
			}

			data = strings.Trim(data, "\n\r")
			client.Log(1, "client.UpstreamRecv <- %s", data)
			client.UpstreamRecv <- data
		}

		client.SendClientSignal("state", "closed")
		client.StartShutdown("upstream_closed")
		upstream.Close()
		if client.IrcState.RemotePort > 0 {
			c.Gateway.identdServ.RemoveIdent(client.IrcState.LocalPort, client.IrcState.RemotePort, "")
		}
	}()
}

func (c *Client) processLineToUpstream(data string) {
	client := c
	upstreamConfig := c.UpstreamConfig

	if strings.HasPrefix(data, "PASS ") && c.SentPass {
		// Hijack the PASS command if we already sent a pass command
		return
	} else if strings.HasPrefix(data, "USER ") {
		// Hijack the USER command as we may have some overrides
		data = fmt.Sprintf(
			"USER %s 0 * :%s",
			client.IrcState.Username,
			client.IrcState.RealName,
		)
	} else if strings.HasPrefix(strings.ToUpper(data), "QUIT ") {
		client.SeenQuit = true
	}

	message, _ := irc.ParseLine(data)

	hook := &HookIrcLine{
		Client:         client,
		UpstreamConfig: upstreamConfig,
		Line:           data,
		Message:        message,
		ToServer:       true,
	}
	hook.Dispatch("irc.line")
	if hook.Halt {
		return
	}

	// Plugins may have modified the data
	data = hook.Line

	client.Log(1, "->upstream: %s", data)
	data = utf8ToOther(data, client.Encoding)
	if data == "" {
		client.Log(1, "Failed to encode into '%s'. Dropping data", c.Encoding)
		return
	}

	client.upstream.Write([]byte(data + "\r\n"))
}

func (c *Client) handleLineFromUpstream(data string) {
	client := c
	upstreamConfig := c.UpstreamConfig

	message, _ := irc.ParseLine(data)

	hook := &HookIrcLine{
		Client:         client,
		UpstreamConfig: upstreamConfig,
		Line:           data,
		Message:        message,
		ToServer:       false,
	}
	hook.Dispatch("irc.line")
	if hook.Halt {
		return
	}

	// Plugins may have modified the data
	data = hook.Line

	if data == "" {
		return
	}

	client.Log(1, "upstream->: %s", data)

	data = ensureUtf8(data, client.Encoding)
	if data == "" {
		client.Log(1, "Failed to decode as 'UTF-8'. Dropping data")
		return
	}

	data = client.ProcessLineFromUpstream(data)
	if data == "" {
		return
	}

	client.SendClientSignal("data", data)
}

func typeOfErr(err error) string {
	if err == nil {
		return ""
	}

	if netError, ok := err.(net.Error); ok && netError.Timeout() {
		return "timeout"
	}

	switch t := err.(type) {
	case *proxy.ConnError:
		switch t.Type {
		case "conn_reset":
			return ""
		case "conn_refused":
			return "refused"
		case "not_found":
			return "unknown_host"
		case "conn_timeout":
			return "timeout"
		default:
			return ""
		}

	case *net.OpError:
		if t.Op == "dial" {
			return "unknown_host"
		} else if t.Op == "read" {
			return "refused"
		}

	case syscall.Errno:
		if t == syscall.ECONNREFUSED {
			return "refused"
		}
	}

	return ""
}

// Handle lines sent from the client
func (c *Client) clientLineWorker() {
ReadLoop:
	for {
		select {
		case clientData, ok := <-c.ThrottledRecv.Output:
			if !ok {
				c.Log(1, "client.Recv closed")
				break ReadLoop
			}

			c.Log(1, "client->: %s", clientData)

			clientLine, err := c.ProcessLineFromClient(clientData)
			if err == nil && clientLine != "" {
				c.UpstreamSendIn <- clientLine
			}

		case line, ok := <-c.UpstreamSendOut:
			if !ok {
				c.Log(1, "client.UpstreamSend closed")
				break ReadLoop
			}
			c.processLineToUpstream(line)

		case upstreamData, ok := <-c.UpstreamRecv:
			c.Log(1, "<-c.UpstreamRecv: %s", upstreamData)
			if !ok {
				c.Log(1, "client.UpstreamRecv closed")
				break ReadLoop
			}

			c.handleLineFromUpstream(upstreamData)
		}
	}

	c.Log(1, "leaving clientLineWorker")

	// close(c.UpstreamSend)
}

// configureUpstream - Generate an upstream configuration from the information set on the client instance
func (c *Client) configureUpstream() ConfigUpstream {
	upstreamConfig := ConfigUpstream{}
	upstreamConfig.Hostname = c.DestHost
	upstreamConfig.Port = c.DestPort
	upstreamConfig.TLS = c.DestTLS
	upstreamConfig.Timeout = c.Gateway.Config.GatewayTimeout
	upstreamConfig.Throttle = c.Gateway.Config.GatewayThrottle
	upstreamConfig.WebircPassword = c.Gateway.findWebircPassword(c.DestHost)

	return upstreamConfig
}

func (c *Client) buildWebircTags() string {
	str := ""
	for key, val := range c.Tags {
		if str != "" {
			str += " "
		}

		if val == "" {
			str += key
		} else {
			str += key + "=" + val
		}
	}

	return str
}

func (c *Client) setState(state string) {
	c.State = state

	hook := &HookClientState{
		Client:    c,
		Connected: c.State != ClientStateEnding,
	}
	hook.Dispatch("client.state")
}
