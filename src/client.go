package main

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
	"webircgateway/irc"

	"golang.org/x/net/html/charset"
)

// Client - Connecting client struct
type Client struct {
	id              int
	ShuttingDown    bool
	Recv            chan string
	Send            chan string
	UpstreamSend    chan string
	signalClose     chan string
	upstreamSignals chan string
	UpstreamStarted bool
	remoteAddr      string
	remoteHostname  string
	remotePort      int
	destHost        string
	destPort        int
	destTLS         bool
	ircState        irc.State
	encoding        string
}

var nextClientID = 1

// NewClient - Makes a new client
func NewClient() *Client {
	thisID := nextClientID
	nextClientID++

	c := &Client{
		id:              thisID,
		Recv:            make(chan string, 50),
		Send:            make(chan string, 50),
		UpstreamSend:    make(chan string, 50),
		signalClose:     make(chan string),
		upstreamSignals: make(chan string),
		encoding:        "UTF-8",
	}
	return c
}

// Log - Log a line of text with context of this client
func (c *Client) Log(level int, format string, args ...interface{}) {
	if level < Config.logLevel {
		return
	}

	levels := [...]string{"L_DEBUG", "L_INFO", "L_WARN"}
	log.Printf("%s client:%d %s", levels[level-1], c.id, fmt.Sprintf(format, args...))
}

func (c *Client) StartShutdown(reason string) {
	c.Log(1, "StartShutdown(%s) ShuttingDown=%t", reason, c.ShuttingDown)
	if !c.ShuttingDown {
		c.ShuttingDown = true
		c.signalClose <- reason
		close(c.signalClose)
		close(c.upstreamSignals)
	}
}

func (c *Client) UpstreamSignal(signal string) {
	c.Log(1, "UpstreamSignal(%s) ShuttingDown=%t", signal, c.ShuttingDown)
	if !c.ShuttingDown {
		// Not all client transports listen for upstream signals, so don't block waiting
		select {
		case c.upstreamSignals <- signal:
		default:
		}
	}
}

// Handle - Handle the lifetime of the client
func (c *Client) Handle() {

	go c.clientLineWorker()

	closeReason := <-c.signalClose

	switch closeReason {
	case "upstream_closed":
		c.Log(2, "Upstream closed the connection")
	case "err_connecting_upstream":
	case "err_no_upstream":
		// Error has been logged already
	case "client_closed":
		c.Log(2, "Client disconnected")
	default:
		c.Log(2, "Closed: %s", closeReason)
	}
}

func (c *Client) connectUpstream() {
	client := c

	c.UpstreamStarted = true

	var upstreamConfig ConfigUpstream

	if client.destHost == "" {
		log.Println("Using pre-set upstream")
		var err error
		upstreamConfig, err = findUpstream()
		if err != nil {
			client.Log(3, "No upstreams available")
			client.StartShutdown("err_no_upstream")
			return
		}
	} else {
		log.Println("Using client given upstream")
		upstreamConfig = ConfigUpstream{}
		upstreamConfig.Hostname = client.destHost
		upstreamConfig.Port = client.destPort
		upstreamConfig.TLS = client.destTLS
		upstreamConfig.Timeout = Config.gatewayTimeout
		upstreamConfig.Throttle = Config.gatewayThrottle
		upstreamConfig.WebircPassword = findWebircPassword(client.destHost)
	}

	dialer := net.Dialer{}
	dialer.Timeout = time.Second * time.Duration(upstreamConfig.Timeout)

	upstreamStr := fmt.Sprintf("%s:%d", upstreamConfig.Hostname, upstreamConfig.Port)
	var upstream net.Conn
	var connErr error

	upstream, connErr = dialer.Dial("tcp", upstreamStr)

	if connErr != nil {
		client.Log(3, "Error connecting to the upstream IRCd. %s", connErr.Error())
		client.UpstreamSignal("closed")
		close(client.Send)
		client.StartShutdown("err_connecting_upstream")
		return
	}

	// Keep track of the upstreams local and remote port numbers
	_, lPortStr, _ := net.SplitHostPort(upstream.LocalAddr().String())
	client.ircState.LocalPort, _ = strconv.Atoi(lPortStr)
	_, rPortStr, _ := net.SplitHostPort(upstream.RemoteAddr().String())
	client.ircState.RemotePort, _ = strconv.Atoi(rPortStr)

	// Add the ports into the identd before possible TLS handshaking. If we do it after then
	// there's a good chance the identd lookup will occur before the handshake has finished
	if Config.identd {
		identdServ.AddIdent(client.ircState.LocalPort, client.ircState.RemotePort, "boo")
	}

	if upstreamConfig.TLS {
		tlsConfig := &tls.Config{InsecureSkipVerify: true}
		tlsConn := tls.Client(upstream, tlsConfig)
		err := tlsConn.Handshake()
		if err != nil {
			client.Log(3, "Error connecting to the upstream IRCd. %s", err.Error())
			client.UpstreamSignal("closed")
			close(client.Send)
			client.StartShutdown("err_connecting_upstream")
			return
		}

		upstream = net.Conn(tlsConn)
	}

	// Send any WEBIRC lines
	if upstreamConfig.WebircPassword != "" {
		webircLine := fmt.Sprintf(
			"WEBIRC %s websocketgateway %s %s\n",
			upstreamConfig.WebircPassword,
			client.remoteHostname,
			client.remoteAddr,
		)
		client.Log(1, "->upstream: %s", webircLine)
		upstream.Write([]byte(webircLine))
	} else {
		client.Log(1, "No webirc to send")
	}

	client.UpstreamSignal("connected")

	// Data from client to upstream
	go func() {
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

			client.Log(1, "->upstream: %s", data)
			data = utf8ToOther(data, client.encoding)
			if data == "" {
				client.Log(1, "Failed to encode into '%s'. Dropping data", c.encoding)
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
		reader := bufio.NewReader(upstream)
		for {
			data, err := reader.ReadString('\n')
			if err != nil {
				break
			}

			client.Log(1, "upstream->: %s", data)

			data = ensureUtf8(data, client.encoding)
			if data == "" {
				client.Log(1, "Failed to encode into 'UTF-8'. Dropping data")
				continue
			}

			client.Send <- data
		}

		client.UpstreamSignal("closed")
		client.StartShutdown("upstream_closed")
		close(client.Send)
		upstream.Close()

		identdServ.RemoveIdent(client.ircState.LocalPort, client.ircState.RemotePort)
	}()
}

// Handle lines sent from the client
func (c *Client) clientLineWorker() {
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
	if strings.ToUpper(message.Command) == "USER" {
		if len(message.Params) < 4 {
			return line, errors.New("Invalid USER line")
		}

		if Config.clientUsername != "" {
			message.Params[0] = Config.clientUsername
			message.Params[0] = strings.Replace(message.Params[0], "%i", ipv4ToHex(c.remoteAddr), -1)
			message.Params[0] = strings.Replace(message.Params[0], "%h", c.remoteHostname, -1)
		}
		if Config.clientRealname != "" {
			message.Params[3] = Config.clientRealname
			message.Params[3] = strings.Replace(message.Params[3], "%i", ipv4ToHex(c.remoteAddr), -1)
			message.Params[3] = strings.Replace(message.Params[3], "%h", c.remoteHostname, -1)
		}

		line = message.ToLine()

		c.ircState.Username = message.Params[0]
		go c.connectUpstream()
	}

	if strings.ToUpper(message.Command) == "ENCODING" {
		if len(message.Params) > 0 {
			encoding, _ := charset.Lookup(message.Params[0])
			if encoding == nil {
				c.Log(1, "Requested unknown encoding, %s", message.Params[0])
			} else {
				c.encoding = message.Params[0]
				c.Log(1, "Set encoding to %s", message.Params[0])
			}
		}

		// Don't send the ENCODING command upstream
		return "", nil
	}

	if strings.ToUpper(message.Command) == "HOST" && !c.UpstreamStarted {
		// HOST irc.network.net:6667
		// HOST irc.network.net:+6667

		if !Config.gateway {
			return "", nil
		}

		addr := message.Params[0]
		if addr == "" {
			c.Send <- "ERROR :Missing host"
			c.StartShutdown("missing_host")
			return "", nil
		}

		// Parse host:+port into the c.dest* vars
		portSep := strings.Index(addr, ":")
		if portSep == -1 {
			c.destHost = addr
			c.destPort = 6667
			c.destTLS = false
		} else {
			c.destHost = addr[0:portSep]
			portParam := addr[portSep+1:]
			if portParam[0:1] == "+" {
				c.destTLS = true
				c.destPort, err = strconv.Atoi(portParam[1:])
				if err != nil {
					c.destPort = 6697
				}
			} else {
				c.destPort, err = strconv.Atoi(portParam[0:])
				if err != nil {
					c.destPort = 6667
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
	if len(Config.remoteOrigins) == 0 {
		return true
	}

	foundMatch := false

	for _, originMatch := range Config.remoteOrigins {
		if originMatch.Match(originHeader) {
			log.Printf("%s = %s", originHeader, originMatch)
			foundMatch = true
			break
		}
	}

	return foundMatch
}

func findUpstream() (ConfigUpstream, error) {
	var ret ConfigUpstream

	if len(Config.upstreams) == 0 {
		return ret, errors.New("No upstreams available")
	}

	randIdx := rand.Intn(len(Config.upstreams))
	ret = Config.upstreams[randIdx]

	return ret, nil
}

func ipv4ToHex(ip string) string {
	parts := strings.Split(ip, ".")
	for idx, part := range parts {
		num, _ := strconv.Atoi(part)
		parts[idx] = fmt.Sprintf("%x", num)
		if len(parts[idx]) == 1 {
			parts[idx] = "0" + parts[idx]
		}
	}

	return strings.Join(parts, "")
}

func findWebircPassword(ircHost string) string {
	pass, exists := Config.gatewayWebircPassword[strings.ToLower(ircHost)]
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
