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
	id             int
	Recv           chan string
	Send           chan string
	signalClose    chan string
	remoteAddr     string
	remoteHostname string
	remotePort     int
}

var nextClientID = 1

// NewClient - Makes a new client
func NewClient() Client {
	thisID := nextClientID
	nextClientID++

	c := Client{
		id:          thisID,
		Recv:        make(chan string, 50),
		Send:        make(chan string, 50),
		signalClose: make(chan string),
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

	upstreamConfig, err := findUpstream()
	if err != nil {
		client.Log(3, "No upstreams available")
		client.signalClose <- "err_no_upstream"
		return
	}

	upstreamTimeout := time.Second * time.Duration(upstreamConfig.Timeout)
	upstreamStr := fmt.Sprintf("%s:%d", upstreamConfig.Hostname, upstreamConfig.Port)

	dialer := net.Dialer{}
	dialer.Timeout = upstreamTimeout

	var upstream net.Conn

	if upstreamConfig.TLS {
		upstream, err = tls.DialWithDialer(&dialer, "tcp", upstreamStr, nil)

	} else {
		upstream, err = dialer.Dial("tcp", upstreamStr)
	}

	if err != nil {
		client.Log(3, "Error connecting to the upstream IRCd. %s", err.Error())
		client.signalClose <- "err_connecting_upstream"
		return
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

	// Data from client to upstream
	go func() {
		for {
			data, ok := <-client.Recv
			if !ok {
				client.Log(1, "connectUpstream() got error from channel")
				break
			}

			// Some IRC lines such as USER commands may have some parameter replacements
			line, err := clientLineReplacements(*client, data)
			if err != nil {
				client.signalClose <- "invalid_client_line"
				break
			}

			client.Log(1, "->upstream: %s", line)
			upstream.Write([]byte(line + "\n"))
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

		close(client.Send)
		client.signalClose <- "upstream_closed"
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
