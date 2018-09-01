package webircgateway

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"

	"sync"

	jwt "github.com/dgrijalva/jwt-go"
	"github.com/kiwiirc/webircgateway/pkg/irc"
	"github.com/kiwiirc/webircgateway/pkg/proxy"
	"github.com/kiwiirc/webircgateway/pkg/recaptcha"
	"golang.org/x/net/html/charset"
)

const (
	// ClientStateIdle - Client connected and just sat there
	ClientStateIdle = "idle"
	// ClientStateConnecting - Connecting upstream
	ClientStateConnecting = "connecting"
	// ClientStateRegistered - Registering to the IRC network
	ClientStateRegistering = "registering"
	// ClientStateConnected - Connected upstream
	ClientStateConnected = "connected"
	// ClientStateEnding - Client is ending its connection
	ClientStateEnding = "ending"
)

// The upstream connection object may be either a TCP client or a KiwiProxy
// instance. Create a common interface we can use that satisfies either
// case.
type ConnInterface interface {
	io.Reader
	io.Writer
	Close() error
}

type ClientSignal [3]string

// Client - Connecting client struct
type Client struct {
	Gateway          *Gateway
	Id               int
	State            string
	EndWG            sync.WaitGroup
	shuttingDownLock sync.Mutex
	shuttingDown     bool
	Recv             chan string
	UpstreamSend     chan string
	UpstreamStarted  bool
	UpstreamConfig   *ConfigUpstream
	RemoteAddr       string
	RemoteHostname   string
	RemotePort       int
	DestHost         string
	DestPort         int
	DestTLS          bool
	IrcState         *irc.State
	Encoding         string
	// Tags get passed upstream via the WEBIRC command
	Tags map[string]string
	// Captchas may be needed to verify a client
	Verified bool
	// Signals for the transport to make use of (data, connection state, etc)
	Signals  chan ClientSignal
	Features struct {
		Messagetags bool
		Metadata    bool
		ExtJwt      bool
	}
}

var nextClientID = 1

// NewClient - Makes a new client
func NewClient(gateway *Gateway) *Client {
	thisID := nextClientID
	nextClientID++

	c := &Client{
		Gateway:      gateway,
		Id:           thisID,
		State:        ClientStateIdle,
		Recv:         make(chan string, 50),
		UpstreamSend: make(chan string, 50),
		Encoding:     "UTF-8",
		Signals:      make(chan ClientSignal, 50),
		Tags:         make(map[string]string),
		IrcState:     irc.NewState(),
	}

	// Auto verify the client if it's not needed
	if !gateway.Config.RequiresVerification {
		c.Verified = true
	}

	go c.clientLineWorker()

	// Add to the clients maps and wait until everything has been marked
	// as completed (several routines add themselves to EndWG so that we can catch
	// when they are all completed)
	gateway.Clients.Set(string(c.Id), c)
	go func() {
		time.Sleep(time.Second * 3)
		c.EndWG.Wait()
		gateway.Clients.Remove(string(c.Id))
	}()

	return c
}

// Log - Log a line of text with context of this client
func (c *Client) Log(level int, format string, args ...interface{}) {
	prefix := fmt.Sprintf("client:%d ", c.Id)
	c.Gateway.Log(level, prefix+format, args...)
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
		c.shuttingDown = true
		c.State = ClientStateEnding

		switch reason {
		case "upstream_closed":
			c.Log(2, "Upstream closed the connection")
		case "err_connecting_upstream":
		case "err_no_upstream":
			// Error has been logged already
		case "client_closed":
			c.Log(2, "Client disconnected")
		default:
			c.Log(2, "Closed: %s", reason)
		}

		close(c.Signals)
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

	client.State = ClientStateConnecting

	upstream, upstreamErr := client.makeUpstreamConnection()
	if upstreamErr != nil {
		// Error handling was already managed in makeUpstreamConnection()
		return
	}

	client.State = ClientStateRegistering

	client.writeWebircLines(upstream)
	client.SendClientSignal("state", "connected")
	client.proxyData(upstream)
}

func (c *Client) makeUpstreamConnection() (ConnInterface, error) {
	client := c
	upstreamConfig := c.UpstreamConfig

	var connection ConnInterface

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

		connection = ConnInterface(conn)
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

		connection = ConnInterface(conn)
	}

	return connection, nil
}

func (c *Client) writeWebircLines(upstream ConnInterface) {
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

func (c *Client) proxyData(upstream ConnInterface) {
	client := c
	upstreamConfig := c.UpstreamConfig

	// Data from client to upstream
	go func() {
		client.EndWG.Add(1)
		defer func() {
			client.EndWG.Done()
		}()

		var writeThrottle time.Duration
		if upstreamConfig.Throttle > 0 {
			writeThrottle = time.Duration(int64(time.Second) / int64(upstreamConfig.Throttle))
		} else {
			writeThrottle = 0
		}

		for {
			data, ok := <-client.UpstreamSend
			if !ok {
				client.Log(1, "connectUpstream() client.UpstreamSend closed")
				break
			}

			// Hijack the USER command as we may have some overrides
			if strings.HasPrefix(data, "USER ") {
				data = fmt.Sprintf(
					"USER %s 0 * :%s",
					client.IrcState.Username,
					client.IrcState.RealName,
				)
			}

			hook := &HookIrcLine{
				Client:         client,
				UpstreamConfig: upstreamConfig,
				Line:           data,
				ToServer:       true,
			}
			hook.Dispatch("irc.line")
			if hook.Halt {
				continue
			}

			// Plugins may have modified the data
			data = hook.Line

			client.Log(1, "->upstream: %s", data)
			data = utf8ToOther(data, client.Encoding)
			if data == "" {
				client.Log(1, "Failed to encode into '%s'. Dropping data", c.Encoding)
				continue
			}

			upstream.Write([]byte(data + "\r\n"))

			if writeThrottle > 0 {
				time.Sleep(writeThrottle)
			}
		}

		upstream.Close()
	}()

	// Data from upstream to client
	go func() {
		client.EndWG.Add(1)
		defer func() {
			client.EndWG.Done()
		}()

		reader := bufio.NewReader(upstream)
		for {
			data, err := reader.ReadString('\n')
			if err != nil {
				break
			}

			data = strings.Trim(data, "\n\r")

			hook := &HookIrcLine{
				Client:         client,
				UpstreamConfig: upstreamConfig,
				Line:           data,
				ToServer:       false,
			}
			hook.Dispatch("irc.line")
			if hook.Halt {
				continue
			}

			// Plugins may have modified the data
			data = hook.Line

			if data == "" {
				continue
			}

			client.Log(1, "upstream->: %s", data)

			data = ensureUtf8(data, client.Encoding)
			if data == "" {
				client.Log(1, "Failed to encode into 'UTF-8'. Dropping data")
				continue
			}

			data = client.ProcessLineFromUpstream(data)
			if data == "" {
				return
			}

			client.SendClientSignal("data", data)
		}

		client.SendClientSignal("state", "closed")
		client.StartShutdown("upstream_closed")
		upstream.Close()
		if client.IrcState.RemotePort > 0 {
			c.Gateway.identdServ.RemoveIdent(client.IrcState.LocalPort, client.IrcState.RemotePort, "")
		}
	}()
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
	c.EndWG.Add(1)
	defer func() {
		c.EndWG.Done()
	}()

	for {
		data, ok := <-c.Recv
		if !ok {
			c.Log(1, "clientLineWorker() client.Recv closed")
			break
		}

		c.Log(1, "ws->: %s", data)

		// Some IRC lines such as USER commands may have some parameter replacements
		line, err := c.ProcesIncomingLine(data)
		if err == nil && line != "" {
			c.UpstreamSend <- line
		}
	}

	close(c.UpstreamSend)
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

// ProcessLineFromUpstream - Processes and makes any changes to a line of data sent from an upstream
func (c *Client) ProcessLineFromUpstream(data string) string {
	client := c

	m, _ := irc.ParseLine(data)
	if m == nil {
		return data
	}

	pLen := len(m.Params)

	if pLen > 0 && m.Command == "NICK" && m.Prefix.Nick == c.IrcState.Nick {
		client.IrcState.Nick = m.Params[0]
	}
	if pLen > 0 && m.Command == "001" {
		client.IrcState.Nick = m.Params[0]
		client.State = ClientStateConnected
	}
	if pLen > 0 && m.Command == "005" {
		// If EXTJWT is supported by the IRC server, disable it here
		foundExtJwt := false
		for _, param := range m.Params {
			if strings.HasPrefix(param, "EXTJWT") {
				foundExtJwt = true
			}
		}

		if foundExtJwt {
			c.Features.ExtJwt = false
		}
	}
	if pLen > 0 && m.Command == "JOIN" && m.Prefix.Nick == c.IrcState.Nick {
		channel := irc.NewStateChannel(m.GetParam(0, ""))
		c.IrcState.SetChannel(channel)
	}
	if pLen > 0 && m.Command == "PART" && m.Prefix.Nick == c.IrcState.Nick {
		c.IrcState.RemoveChannel(m.GetParam(0, ""))
	}
	if pLen > 0 && m.Command == "QUIT" && m.Prefix.Nick == c.IrcState.Nick {
		c.IrcState.ClearChannels()
	}
	// :prawnsalad!prawn@kiwiirc/prawnsalad MODE #kiwiirc-dev +oo notprawn kiwi-n75
	if pLen > 0 && m.Command == "MODE" {
		if strings.HasPrefix(m.GetParam(0, ""), "#") {
			channelName := m.GetParam(0, "")
			modes := m.GetParam(1, "")

			channel := c.IrcState.GetChannel(channelName)
			if channel != nil {
				channel = irc.NewStateChannel(channelName)
				c.IrcState.SetChannel(channel)
			}

			adding := false
			paramIdx := 1
			for i := 0; i < len(modes); i++ {
				mode := string(modes[i])

				if mode == "+" {
					adding = true
				} else if mode == "-" {
					adding = false
				} else {
					paramIdx++
					param := m.GetParam(paramIdx, "")
					if strings.ToLower(param) == strings.ToLower(c.IrcState.Nick) {
						if adding {
							channel.Modes[mode] = ""
						} else {
							delete(channel.Modes, mode)
						}
					}
				}
			}
		}
	}

	// If upstream reports that it supports message-tags natively, disable the wrapping of this feature for
	// this client
	if pLen >= 3 &&
		strings.ToUpper(m.Command) == "CAP" &&
		strings.ToUpper(m.Params[1]) == "LS" {
		// The CAPs could be param 2 or 3 depending on if were using multiple lines to list them all.
		caps := ""
		if pLen >= 4 && m.Params[2] == "*" {
			caps = strings.ToUpper(m.Params[3])
		} else {
			caps = strings.ToUpper(m.Params[2])
		}

		if strings.Contains(caps, "DRAFT/MESSAGE-TAGS-0.2") {
			c.Log(1, "Upstream already supports Messagetags, disabling feature")
			c.Features.Messagetags = false
		}
	}

	if m != nil && client.Features.Messagetags && c.Gateway.messageTags.CanMessageContainClientTags(m) {
		// If we have any message tags stored for this message from a previous PRIVMSG sent
		// by a client, add them back in
		mTags, mTagsExists := c.Gateway.messageTags.GetTagsFromMessage(client, m.Prefix.Nick, m)
		if mTagsExists {
			for k, v := range mTags.Tags {
				m.Tags[k] = v
			}

			data = m.ToLine()
		}
	}

	return data
}

// ProcesIncomingLine - Processes and makes any changes to a line of data sent from a client
func (c *Client) ProcesIncomingLine(line string) (string, error) {
	message, err := irc.ParseLine(line)
	// Just pass any random data upstream
	if err != nil {
		return line, nil
	}

	maybeConnectUpstream := func() {
		if !c.UpstreamStarted && c.IrcState.Username != "" && c.Verified {
			go c.connectUpstream()
		}
	}

	if !c.Verified && strings.ToUpper(message.Command) == "CAPTCHA" {
		verified := false
		if len(message.Params) >= 1 {
			captcha := recaptcha.R{
				Secret: c.Gateway.Config.ReCaptchaSecret,
			}

			verified = captcha.VerifyResponse(message.Params[0])
		}

		if !verified {
			c.SendIrcError("Invalid captcha")
			c.StartShutdown("unverifed")
		} else {
			c.Verified = true
			maybeConnectUpstream()
		}

		return "", nil
	}

	// USER <username> <hostname> <servername> <realname>
	if strings.ToUpper(message.Command) == "USER" && !c.UpstreamStarted {
		if len(message.Params) < 4 {
			return line, errors.New("Invalid USER line")
		}

		if c.Gateway.Config.ClientUsername != "" {
			message.Params[0] = makeClientReplacements(c.Gateway.Config.ClientUsername, c)
		}
		if c.Gateway.Config.ClientRealname != "" {
			message.Params[3] = makeClientReplacements(c.Gateway.Config.ClientRealname, c)
		}

		line = message.ToLine()

		c.IrcState.Username = message.Params[0]
		c.IrcState.RealName = message.Params[3]

		maybeConnectUpstream()
	}

	if strings.ToUpper(message.Command) == "ENCODING" {
		if len(message.Params) > 0 {
			encoding, _ := charset.Lookup(message.Params[0])
			if encoding == nil {
				c.Log(1, "Requested unknown encoding, %s", message.Params[0])
			} else {
				c.Encoding = message.Params[0]
				c.Log(1, "Set encoding to %s", message.Params[0])
			}
		}

		// Don't send the ENCODING command upstream
		return "", nil
	}

	if strings.ToUpper(message.Command) == "HOST" && !c.UpstreamStarted {
		// HOST irc.network.net:6667
		// HOST irc.network.net:+6667

		if !c.Gateway.Config.Gateway {
			return "", nil
		}

		if len(message.Params) == 0 {
			return "", nil
		}

		addr := message.Params[0]
		if addr == "" {
			c.SendIrcError("Missing host")
			c.StartShutdown("missing_host")
			return "", nil
		}

		// Parse host:+port into the c.dest* vars
		portSep := strings.Index(addr, ":")
		if portSep == -1 {
			c.DestHost = addr
			c.DestPort = 6667
			c.DestTLS = false
		} else {
			c.DestHost = addr[0:portSep]
			portParam := addr[portSep+1:]
			if len(portParam) > 0 && portParam[0:1] == "+" {
				c.DestTLS = true
				c.DestPort, err = strconv.Atoi(portParam[1:])
				if err != nil {
					c.DestPort = 6697
				}
			} else {
				c.DestPort, err = strconv.Atoi(portParam[0:])
				if err != nil {
					c.DestPort = 6667
				}
			}
		}

		// Don't send the HOST command upstream
		return "", nil
	}

	// If the client supports CAP, assume the client also supports parsing MessageTags
	// When upstream replies with its CAP listing, we check if message-tags is supported by the IRCd already and if so,
	// we disable this feature flag again to use the IRCds native support.
	if strings.ToUpper(message.Command) == "CAP" && len(message.Params) >= 0 && strings.ToUpper(message.Params[0]) == "LS" {
		c.Log(1, "Enabling client Messagetags feature")
		c.Features.Messagetags = true
	}

	// Check for any client message tags so that we can store them for replaying to other clients
	if c.Features.Messagetags && c.Gateway.messageTags.CanMessageContainClientTags(message) {
		c.Gateway.messageTags.AddTagsFromMessage(c, c.IrcState.Nick, message)
		// Prevent any client tags heading upstream
		for k := range message.Tags {
			if k[0] == '+' {
				delete(message.Tags, k)
			}
		}

		line = message.ToLine()
	}

	if c.Features.Messagetags && message.Command == "TAGMSG" {
		if len(message.Params) == 0 {
			return "", nil
		}

		// We can't be 100% sure what this users correct mask is, so just send the nick
		message.Prefix.Nick = c.IrcState.Nick
		message.Prefix.Hostname = ""
		message.Prefix.Username = ""

		thisHost := strings.ToLower(c.UpstreamConfig.Hostname)
		target := message.Params[0]
		for val := range c.Gateway.Clients.Iter() {
			curClient := val.Val.(*Client)
			sameHost := strings.ToLower(curClient.UpstreamConfig.Hostname) == thisHost
			if !sameHost {
				continue
			}

			// Only send the message on to either the target nick, or the clients in a set channel
			curNick := strings.ToLower(curClient.IrcState.Nick)
			if target != curNick && !curClient.IrcState.HasChannel(target) {
				continue
			}

			curClient.SendClientSignal("data", message.ToLine())
		}

		return "", nil
	}

	if c.Features.ExtJwt && strings.ToUpper(message.Command) == "EXTJWT" {
		tokenFor := message.GetParam(0, "")

		tokenM := irc.Message{}
		tokenM.Command = "EXTJWT"
		tokenData := jwt.MapClaims{
			"exp":         time.Now().UTC().Add(1 * time.Minute).Unix(),
			"iss":         c.UpstreamConfig.Hostname,
			"nick":        c.IrcState.Nick,
			"account":     "",
			"net_modes":   []string{},
			"channel":     "",
			"joined":      false,
			"time_joined": 0,
			"modes":       []string{},
		}

		if tokenFor == "" {
			tokenM.Params = append(tokenM.Params, "*")
		} else {
			tokenM.Params = append(tokenM.Params, tokenFor)

			tokenForChan := c.IrcState.GetChannel(tokenFor)
			if tokenForChan != nil {
				tokenData["time_joined"] = tokenForChan.Joined.Unix()
				tokenData["channel"] = tokenForChan.Name
				tokenData["joined"] = true

				modes := []string{}
				for mode := range tokenForChan.Modes {
					modes = append(modes, mode)
				}
				tokenData["modes"] = modes
			} else {
				tokenData["channel"] = tokenFor
			}
		}

		token := jwt.NewWithClaims(jwt.SigningMethodHS256, tokenData)
		tokenSigned, tokenSignedErr := token.SignedString([]byte(c.Gateway.Config.Secret))
		if tokenSignedErr != nil {
			c.Log(3, "Error creating JWT token. %s", tokenSignedErr.Error())
			println(tokenSignedErr.Error())
		}

		tokenM.Params = append(tokenM.Params, tokenSigned)

		c.SendClientSignal("data", tokenM.ToLine())

		return "", nil
	}

	return line, nil
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
