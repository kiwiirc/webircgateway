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
)

// Client - Connecting client struct
type Client struct {
	id              int
	ShuttingDown    bool
	Recv            chan string
	Send            chan string
	signalClose     chan string
	upstreamSignals chan string
	UpstreamStarted bool
	remoteAddr      string
	remoteHostname  string
	remotePort      int
	destHost        string
	destPort        int
	destTLS         bool
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
		signalClose:     make(chan string),
		upstreamSignals: make(chan string),
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
	// Start a goroutine here as connecting upstream is blocking. client.SignalClose will
	// catch any reasons to stop
	go c.connectUpstream()

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
			data, ok := <-client.Recv
			if !ok {
				client.Log(1, "connectUpstream() got error from channel")
				break
			}

			// Some IRC lines such as USER commands may have some parameter replacements
			line, err := clientLineReplacements(*client, data)
			if err != nil {
				client.StartShutdown("invalid_client_line")
				break
			}

			client.Log(1, "->upstream: %s", line)
			upstream.Write([]byte(line + "\r\n"))

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
			client.Send <- data
		}

		client.UpstreamSignal("closed")
		client.StartShutdown("upstream_closed")
		close(client.Send)
		upstream.Close()
	}()
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

func clientLineReplacements(client Client, line string) (string, error) {
	// USER <username> <hostname> <servername> <realname>
	if strings.HasPrefix(line, "USER") {
		parts := strings.Split(line, " ")
		if len(parts) < 5 {
			return line, errors.New("Invalid USER line")
		}

		if Config.clientUsername != "" {
			parts[1] = Config.clientUsername
			parts[1] = strings.Replace(parts[1], "%i", ipv4ToHex(client.remoteAddr), -1)
			parts[1] = strings.Replace(parts[1], "%h", client.remoteHostname, -1)
		}
		if Config.clientRealname != "" {
			parts[4] = ":" + Config.clientRealname
			parts[4] = strings.Replace(parts[4], "%i", ipv4ToHex(client.remoteAddr), -1)
			parts[4] = strings.Replace(parts[4], "%h", client.remoteHostname, -1)
			// We've just set the realname (final param 4) so remove everything else after it
			parts = parts[:5]
		}

		line = strings.Join(parts, " ")
	}

	return line, nil
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
