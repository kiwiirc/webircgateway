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
	"time"
	"unicode/utf8"

	"sync"

	"github.com/kiwiirc/webircgateway/pkg/irc"
	"github.com/kiwiirc/webircgateway/pkg/proxy"
	"github.com/orcaman/concurrent-map"
	"golang.org/x/net/html/charset"
)

type ClientSignal [2]string

// Client - Connecting client struct
type Client struct {
	Id              int
	State           string
	EndWG           sync.WaitGroup
	ShuttingDown    bool
	Recv            chan string
	UpstreamSend    chan string
	UpstreamStarted bool
	RemoteAddr      string
	RemoteHostname  string
	RemotePort      int
	DestHost        string
	DestPort        int
	DestTLS         bool
	IrcState        irc.State
	Encoding        string
	// Signals for the transport to make use of (data, connection state, etc)
	Signals chan ClientSignal
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
		State:        "idle",
		Recv:         make(chan string, 50),
		UpstreamSend: make(chan string, 50),
		Encoding:     "UTF-8",
		Signals:      make(chan ClientSignal, 50),
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

func (c *Client) StartShutdown(reason string) {
	c.Log(1, "StartShutdown(%s) ShuttingDown=%t", reason, c.ShuttingDown)
	if !c.ShuttingDown {
		c.ShuttingDown = true
		c.State = "ending"

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

func (c *Client) SendClientSignal(signal string, arg string) {
	if !c.ShuttingDown {
		c.Signals <- ClientSignal{signal, arg}
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
			client.SendClientSignal("state", "closed")
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
		client.SendClientSignal("state", "closed")
		client.StartShutdown("err_connecting_upstream")
		return
	}

	client.State = "connecting"

	if upstreamConfig.Proxy == nil {
		// Connect directly to the IRCd
		dialer := net.Dialer{}
		dialer.Timeout = time.Second * time.Duration(upstreamConfig.Timeout)

		upstreamStr := fmt.Sprintf("%s:%d", upstreamConfig.Hostname, upstreamConfig.Port)
		conn, connErr := dialer.Dial("tcp", upstreamStr)

		if connErr != nil {
			client.Log(3, "Error connecting to the upstream IRCd. %s", connErr.Error())
			client.SendClientSignal("state", "closed")
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

			identdServ.AddIdent(client.IrcState.LocalPort, client.IrcState.RemotePort, client.IrcState.Username)
		}

		if upstreamConfig.TLS {
			tlsConfig := &tls.Config{InsecureSkipVerify: true}
			tlsConn := tls.Client(conn, tlsConfig)
			err := tlsConn.Handshake()
			if err != nil {
				client.Log(3, "Error connecting to the upstream IRCd. %s", err.Error())
				client.SendClientSignal("state", "closed")
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
			client.Log(3, "Error connecting to the kiwi proxy. %s", dialErr.Error())
			client.SendClientSignal("state", "closed")
			client.StartShutdown("err_connecting_upstream")
			return
		}

		upstream = ConnInterface(conn)
	}

	client.State = "registering"

	// Send any WEBIRC lines
	if upstreamConfig.WebircPassword != "" {
		webircLine := fmt.Sprintf(
			"WEBIRC %s websocketgateway %s %s\n",
			upstreamConfig.WebircPassword,
			client.RemoteHostname,
			client.RemoteAddr,
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
				client.State = "connected"
			}

			client.SendClientSignal("data", data)
		}

		client.SendClientSignal("state", "closed")
		client.StartShutdown("upstream_closed")
		upstream.Close()
		if client.IrcState.RemotePort > 0 {
			identdServ.RemoveIdent(client.IrcState.LocalPort, client.IrcState.RemotePort)
		}
	}()
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

	// USER <username> <hostname> <servername> <realname>
	if strings.ToUpper(message.Command) == "USER" && !c.UpstreamStarted {
		if len(message.Params) < 4 {
			return line, errors.New("Invalid USER line")
		}

		if Config.ClientUsername != "" {
			message.Params[0] = Config.ClientUsername
			message.Params[0] = strings.Replace(message.Params[0], "%i", Ipv4ToHex(c.RemoteAddr), -1)
			message.Params[0] = strings.Replace(message.Params[0], "%h", c.RemoteHostname, -1)
		}
		if Config.ClientRealname != "" {
			message.Params[3] = Config.ClientRealname
			message.Params[3] = strings.Replace(message.Params[3], "%i", Ipv4ToHex(c.RemoteAddr), -1)
			message.Params[3] = strings.Replace(message.Params[3], "%h", c.RemoteHostname, -1)
		}

		line = message.ToLine()

		c.IrcState.Username = message.Params[0]
		c.IrcState.RealName = message.Params[3]
		go c.connectUpstream()
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

	return line, nil
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

func ensureUtf8(s string, fromEncoding string) string {
	if utf8.ValidString(s) {
		return s
	}

	encoding, _ := charset.Lookup(fromEncoding)
	if encoding == nil {
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
