package webircgateway

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"sync"

	"github.com/kiwiirc/webircgateway/pkg/irc"
	"github.com/kiwiirc/webircgateway/pkg/proxy"
	"github.com/kiwiirc/webircgateway/pkg/recaptcha"
	"github.com/orcaman/concurrent-map"
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

type ClientSignal [3]string

// Client - Connecting client struct
type Client struct {
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
	IrcState         irc.State
	Encoding         string
	// Tags get passed upstream via the WEBIRC command
	Tags             map[string]string
	// Captchas may be needed to verify a client
	Verified bool
	// Signals for the transport to make use of (data, connection state, etc)
	Signals chan ClientSignal
	Features struct {
		Messagetags bool
		Metadata bool
	}
}

// Clients hold a map lookup for all the connected clients
var Clients = cmap.New()
var nextClientID = 1

// NewClient - Makes a new client
func NewClient() *Client {
	thisID := nextClientID
	nextClientID++

	c := &Client{
		Id:           thisID,
		State:        ClientStateIdle,
		Recv:         make(chan string, 50),
		UpstreamSend: make(chan string, 50),
		Encoding:     "UTF-8",
		Signals:      make(chan ClientSignal, 50),
		Tags:         make(map[string]string),
	}

	// Auto verify the client if it's not needed
	if !Config.RequiresVerification {
		c.Verified = true
	}

	go c.clientLineWorker()

	// Add to the clients maps and wait until everything has been marked
	// as completed (several routines add themselves to EndWG so that we can catch
	// when they are all completed)
	Clients.Set(string(c.Id), c)
	go func() {
		time.Sleep(time.Second * 3)
		c.EndWG.Wait()
		Clients.Remove(string(c.Id))
	}()

	return c
}

// Log - Log a line of text with context of this client
func (c *Client) Log(level int, format string, args ...interface{}) {
	prefix := fmt.Sprintf("client:%d ", c.Id)
	logOut(level, prefix+format, args...)
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

	// The upstream connection object may be either a TCP client or a KiwiProxy
	// instance. Create a common interface we can use that satisfies either
	// case.
	type ConnInterface interface {
		io.Reader
		io.Writer
		Close() error
	}
	var upstream ConnInterface

	var upstreamConfig ConfigUpstream
	c.UpstreamConfig = &upstreamConfig

	if client.DestHost == "" {
		client.Log(2, "Using pre-set upstream")
		var err error
		upstreamConfig, err = findUpstream()
		if err != nil {
			client.Log(3, "No upstreams available")
			client.SendIrcError("The server has not been configured")
			client.StartShutdown("err_no_upstream")
			return
		}
	} else {
		if !isIrcAddressAllowed(client.DestHost) {
			client.Log(2, "Server %s is not allowed. Closing connection", client.DestHost)
			client.SendIrcError("Not allowed to connect to " + client.DestHost)
			client.SendClientSignal("state", "closed", "err_forbidden")
			client.StartShutdown("err_no_upstream")
			return
		}

		client.Log(2, "Using client given upstream")
		upstreamConfig = configureUpstreamFromClient(c)
	}

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
			return
		}

		// Add the ports into the identd before possible TLS handshaking. If we do it after then
		// there's a good chance the identd lookup will occur before the handshake has finished
		if Config.Identd {
			// Keep track of the upstreams local and remote port numbers
			_, lPortStr, _ := net.SplitHostPort(conn.LocalAddr().String())
			client.IrcState.LocalPort, _ = strconv.Atoi(lPortStr)
			_, rPortStr, _ := net.SplitHostPort(conn.RemoteAddr().String())
			client.IrcState.RemotePort, _ = strconv.Atoi(rPortStr)

			identdServ.AddIdent(client.IrcState.LocalPort, client.IrcState.RemotePort, client.IrcState.Username, "")
		}

		if upstreamConfig.TLS {
			tlsConfig := &tls.Config{InsecureSkipVerify: true}
			tlsConn := tls.Client(conn, tlsConfig)
			err := tlsConn.Handshake()
			if err != nil {
				client.Log(3, "Error connecting to the upstream IRCd. %s", err.Error())
				client.SendClientSignal("state", "closed", "err_tls")
				client.StartShutdown("err_connecting_upstream")
				return
			}

			conn = net.Conn(tlsConn)
		}

		upstream = ConnInterface(conn)
	} else {
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
			return
		}

		upstream = ConnInterface(conn)
	}

	client.State = ClientStateRegistering

	// Send any WEBIRC lines
	if upstreamConfig.WebircPassword != "" {
		gatewayName := "websocketgateway"
		if Config.GatewayName != "" {
			gatewayName = Config.GatewayName
		}
		if upstreamConfig.GatewayName != "" {
			gatewayName = upstreamConfig.GatewayName
		}

		webircTags := buildWebircTags(client)
		if strings.Contains(webircTags, " ") {
			webircTags = ":" + webircTags
		}

		clientHostname := client.RemoteHostname
		if Config.ClientHostname != "" {
			clientHostname = makeClientReplacements(Config.ClientHostname, client)
		}

		remoteAddr := client.RemoteAddr
		// Wrap IPv6 addresses so they can be sent as an individual IRC parameter
		// eg. ::1 would not parse correctly as a parameter, while [::1] will
		if strings.Contains(remoteAddr, ":") {
			remoteAddr = "[" + remoteAddr + "]"
		}

		webircLine := fmt.Sprintf(
			"WEBIRC %s %s %s %s %s\n",
			upstreamConfig.WebircPassword,
			gatewayName,
			clientHostname,
			remoteAddr,
			webircTags,
		)
		client.Log(1, "->upstream: %s", webircLine)
		upstream.Write([]byte(webircLine))
	} else {
		client.Log(1, "No webirc to send")
	}

	client.SendClientSignal("state", "connected")

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
				UpstreamConfig: &upstreamConfig,
				Line:           data,
				ToServer:       true,
			}
			hook.Dispatch("irc.line")
			if hook.Halt {
				continue
			}

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
				UpstreamConfig: &upstreamConfig,
				Line:           data,
				ToServer:       false,
			}
			hook.Dispatch("irc.line")
			if hook.Halt {
				continue
			}

			if data == "" {
				continue
			}

			client.Log(1, "upstream->: %s", data)

			data = ensureUtf8(data, client.Encoding)
			if data == "" {
				client.Log(1, "Failed to encode into 'UTF-8'. Dropping data")
				continue
			}

			m, _ := irc.ParseLine(data)
			pLen := 0
			if m != nil {
				pLen = len(m.Params)
			}

			if pLen > 0 && m.Command == "NICK" {
				client.IrcState.Nick = m.Params[0]
			}
			if pLen > 0 && m.Command == "001" {
				client.IrcState.Nick = m.Params[0]
				client.State = ClientStateConnected
			}

			// If upstream reports that it supports message-tags natively, disable the wrapping of this feature for
			// this client
			if pLen >= 3 &&
			   strings.ToUpper(m.Command) == "CAP" &&
			   strings.ToUpper(m.Params[0]) == "*" &&
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

			if m != nil && client.Features.Messagetags && messageTags.CanMessageContainClientTags(m) {
				// If we have any message tags stored for this message from a previous PRIVMSG sent
				// by a client, add them back in
				mTags, mTagsExists := messageTags.GetTagsFromMessage(client, m.Prefix.Nick, m)
				if mTagsExists {
					for k, v := range mTags.Tags {
						m.Tags[k] = v
					}

					data = m.ToLine()
				}
			}

			client.SendClientSignal("data", data)
		}

		client.SendClientSignal("state", "closed")
		client.StartShutdown("upstream_closed")
		upstream.Close()
		if client.IrcState.RemotePort > 0 {
			identdServ.RemoveIdent(client.IrcState.LocalPort, client.IrcState.RemotePort, "")
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
				Secret: Config.ReCaptchaSecret,
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

		if Config.ClientUsername != "" {
			message.Params[0] = makeClientReplacements(Config.ClientUsername, c)
		}
		if Config.ClientRealname != "" {
			message.Params[3] = makeClientReplacements(Config.ClientRealname, c)
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

		if !Config.Gateway {
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
	if c.Features.Messagetags && messageTags.CanMessageContainClientTags(message) {
		messageTags.AddTagsFromMessage(c, c.IrcState.Nick, message)
		// Prevent any client tags heading upstream
		for k := range message.Tags {
			if k[0] == '+' {
				delete(message.Tags, k)
			}
		}

		line = message.ToLine()
	}

	return line, nil
}

// Username / realname / webirc hostname can all have configurable replacements
func makeClientReplacements(format string, client *Client) string {
	ret := format
	ret = strings.Replace(ret, "%i", Ipv4ToHex(client.RemoteAddr), -1)
	ret = strings.Replace(ret, "%h", client.RemoteHostname, -1)
	return ret
}

func isClientOriginAllowed(originHeader string) bool {
	// Empty list of origins = all origins allowed
	if len(Config.RemoteOrigins) == 0 {
		return true
	}

	// No origin header = running on the same page
	if originHeader == "" {
		return true
	}

	foundMatch := false

	for _, originMatch := range Config.RemoteOrigins {
		if originMatch.Match(originHeader) {
			foundMatch = true
			break
		}
	}

	return foundMatch
}

func isIrcAddressAllowed(addr string) bool {
	// Empty whitelist = all destinations allowed
	if len(Config.GatewayWhitelist) == 0 {
		return true
	}

	foundMatch := false

	for _, addrMatch := range Config.GatewayWhitelist {
		if addrMatch.Match(addr) {
			foundMatch = true
			break
		}
	}

	return foundMatch
}

func findUpstream() (ConfigUpstream, error) {
	var ret ConfigUpstream

	if len(Config.Upstreams) == 0 {
		return ret, errors.New("No upstreams available")
	}

	randIdx := rand.Intn(len(Config.Upstreams))
	ret = Config.Upstreams[randIdx]

	return ret, nil
}

func configureUpstreamFromClient(client *Client) ConfigUpstream {
	upstreamConfig := ConfigUpstream{}
	upstreamConfig.Hostname = client.DestHost
	upstreamConfig.Port = client.DestPort
	upstreamConfig.TLS = client.DestTLS
	upstreamConfig.Timeout = Config.GatewayTimeout
	upstreamConfig.Throttle = Config.GatewayThrottle
	upstreamConfig.WebircPassword = findWebircPassword(client.DestHost)

	return upstreamConfig
}

func Ipv4ToHex(ip string) string {
	var ipParts [4]int
	fmt.Sscanf(ip, "%d.%d.%d.%d", &ipParts[0], &ipParts[1], &ipParts[2], &ipParts[3])
	ipHex := fmt.Sprintf("%02x%02x%02x%02x", ipParts[0], ipParts[1], ipParts[2], ipParts[3])
	return ipHex
}

func findWebircPassword(ircHost string) string {
	pass, exists := Config.GatewayWebircPassword[strings.ToLower(ircHost)]
	if !exists {
		pass = ""
	}

	return pass
}

func buildWebircTags(client *Client) string {
	str := ""
	for key, val := range client.Tags {
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

func ensureUtf8(s string, fromEncoding string) string {
	if utf8.ValidString(s) {
		return s
	}

	encoding, encErr := charset.Lookup(fromEncoding)
	if encoding == nil {
		println("encErr:", encErr)
		return ""
	}

	d := encoding.NewDecoder()
	s2, _ := d.String(s)
	return s2
}

func utf8ToOther(s string, toEncoding string) string {
	if toEncoding == "UTF-8" && utf8.ValidString(s) {
		return s
	}

	encoding, _ := charset.Lookup(toEncoding)
	if encoding == nil {
		return ""
	}

	e := encoding.NewEncoder()
	s2, _ := e.String(s)
	return s2
}

func GetRemoteAddressFromRequest(req *http.Request) net.IP {
	remoteAddr, _, _ := net.SplitHostPort(req.RemoteAddr)

	// Some web listeners such as unix sockets don't get a RemoteAddr, so default to localhost
	if remoteAddr == "" {
		remoteAddr = "127.0.0.1"
	}

	remoteIP := net.ParseIP(remoteAddr)

	isInRange := false
	for _, cidrRange := range Config.ReverseProxies {
		if cidrRange.Contains(remoteIP) {
			isInRange = true
			break
		}
	}

	// If the remoteIP is not in a whitelisted reverse proxy range, don't trust
	// the headers and use the remoteIP as the users IP
	if !isInRange {
		return remoteIP
	}

	headerVal := req.Header.Get("x-forwarded-for")
	ips := strings.Split(headerVal, ",")
	ipStr := strings.Trim(ips[0], " ")
	if ipStr != "" {
		ip := net.ParseIP(ipStr)
		if ip != nil {
			remoteIP = ip
		}
	}

	return remoteIP

}

func isRequestSecure(req *http.Request) bool {
	remoteAddr, _, _ := net.SplitHostPort(req.RemoteAddr)
	remoteIP := net.ParseIP(remoteAddr)

	isInRange := false
	for _, cidrRange := range Config.ReverseProxies {
		if cidrRange.Contains(remoteIP) {
			isInRange = true
			break
		}
	}

	// If the remoteIP is not in a whitelisted reverse proxy range, don't trust
	// the headers and check the request directly
	if !isInRange && req.TLS == nil {
		return false
	} else if !isInRange {
		return true
	}

	headerVal := strings.ToLower(req.Header.Get("x-forwarded-proto"))
	if headerVal == "https" {
		return true
	}

	return false
}
