package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/igm/sockjs-go/sockjs"
)

type Channel struct {
	Conn         sockjs.Session
	Client       *Client
	Id           string
	destHost     string
	destPort     int
	destTLS      bool
	waitForClose chan bool
}

var nextChannelID int

func makeChannel(chanID string, ws sockjs.Session) *Channel {
	client := NewClient()

	remoteAddr, remotePort, _ := net.SplitHostPort(ws.Request().RemoteAddr)
	client.remoteAddr = remoteAddr
	client.remotePort, _ = strconv.Atoi(remotePort)

	clientHostnames, err := net.LookupAddr(client.remoteAddr)
	if err != nil {
		client.remoteHostname = client.remoteAddr
	} else {
		client.remoteHostname = clientHostnames[0]
	}

	client.Log(2, "New kiwi channel from %s %s", client.remoteAddr, client.remoteHostname)

	channel := Channel{
		Id:           chanID,
		Client:       client,
		Conn:         ws,
		waitForClose: make(chan bool),
	}

	go channel.lineWriter()
	return &channel
}

func (c *Channel) Start() {
	go func() {
		for {
			signal, stillOpen := <-c.Client.upstreamSignals
			if !stillOpen {
				break
			} else if signal == "connected" {
				c.Conn.Send(fmt.Sprintf(":%s control connected", c.Id))
			} else if signal == "closed" {
				c.Conn.Send(fmt.Sprintf(":%s control closed", c.Id))
			}
		}
	}()

	// If this channel is requesting a specific upstream host, set it now
	if c.destHost != "" {
		c.Client.destHost = c.destHost
		c.Client.destPort = c.destPort
		c.Client.destTLS = c.destTLS
	}

	c.Client.Handle()

	close(c.Client.Recv)
	close(c.waitForClose)
}

func (c *Channel) handleIncomingLine(line string) {
	message, err := ircParseLine(line)

	// Just pass any random data upstream
	if err != nil {
		c.Client.Recv <- line
		return
	}

	if message.Command == "HOST" && !c.Client.UpstreamStarted {
		// HOST irc.network.net:6667
		// HOST irc.network.net:+6667

		if !Config.gateway {
			c.Conn.Send(fmt.Sprintf(":%s ERROR :Host forbidden", c.Id))
			c.Client.StartShutdown("forbidden_host")
			return
		}

		addr := message.Params[0]
		if addr == "" {
			c.Conn.Send(fmt.Sprintf(":%s ERROR :Missing host", c.Id))
			c.Client.StartShutdown("missing_host")
			return
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

		go c.Start()
		return
	}

	c.Client.Recv <- line
}

func (c *Channel) lineWriter() {
	client := c.Client

	for {
		line, ok := <-client.Send
		if !ok {
			break
		}

		toSend := strings.Trim(line, "\r\n")
		client.Log(1, "->ws: %s", toSend)
		c.Conn.Send(fmt.Sprintf(":%s %s", c.Id, toSend))
	}
}

func kiwiircHTTPHandler() {
	handler := sockjs.NewHandler("/webirc/channelled", sockjs.DefaultOptions, kiwiircHandler)
	http.Handle("/webirc/channelled/", handler)
}

func kiwiircHandler(session sockjs.Session) {
	channels := make(map[string]Channel)

	// Read from sockjs
	go func() {
		for {
			msg, err := session.Recv()
			if err == nil && len(msg) > 0 {
				idEnd := strings.Index(msg, " ")
				if idEnd == -1 {
					// msg is in the form of ":chanId"
					chanID := msg[1:]
					channel, channelExists := channels[chanID]
					if !channelExists {
						channel = *makeChannel(chanID, session)
						channels[chanID] = channel

						// When the channel closes, remove it from the map again
						go func() {
							<-channel.waitForClose
							channels[chanID].Client.Log(2, "Removing channel from connection")
							delete(channels, chanID)
						}()
					}

					session.Send(":" + chanID)

				} else {
					// msg is in the form of ":chanId data"
					chanID := msg[1:idEnd]
					data := msg[idEnd+1:]
					channel, channelExists := channels[chanID]
					if channelExists {
						channel.handleIncomingLine(data)
					}
				}
			} else if err != nil {
				log.Printf("sockjs connection read error: %s", err.Error())
				break
			}
		}

		for _, channel := range channels {
			channel.Client.StartShutdown("client_closed")
		}
	}()
}
