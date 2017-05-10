package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/igm/sockjs-go/sockjs"
)

type Channel struct {
	Conn         sockjs.Session
	Client       *Client
	Id           string
	waitForClose chan bool
}

var nextChannelID int

func makeChannel(chanID string, ws sockjs.Session) *Channel {
	client := NewClient()

	originHeader := strings.ToLower(ws.Request().Header.Get("Origin"))
	if !isClientOriginAllowed(originHeader) {
		client.Log(2, "Origin %s not allowed. Closing connection", originHeader)
		ws.Close(0, "Origin not allowed")
		return nil
	}

	client.remoteAddr = GetRemoteAddressFromRequest(ws.Request()).String()

	clientHostnames, err := net.LookupAddr(client.remoteAddr)
	if err != nil {
		client.remoteHostname = client.remoteAddr
	} else {
		// FQDNs include a . at the end. Strip it out
		client.remoteHostname = strings.Trim(clientHostnames[0], ".")
	}

	client.Log(2, "New kiwi channel from %s %s", client.remoteAddr, client.remoteHostname)

	channel := Channel{
		Id:           chanID,
		Client:       client,
		Conn:         ws,
		waitForClose: make(chan bool),
	}

	go channel.lineWriter()
	go channel.Start()

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

	c.Client.Handle()

	close(c.Client.Recv)
	close(c.waitForClose)
}

func (c *Channel) handleIncomingLine(line string) {
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
	handler := sockjs.NewHandler("/webirc/kiwiirc", sockjs.DefaultOptions, kiwiircHandler)
	http.Handle("/webirc/kiwiirc/", handler)
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
					_, channelExists := channels[chanID]
					if !channelExists {
						channel := makeChannel(chanID, session)
						if channel == nil {
							continue
						}
						channels[chanID] = *channel

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
				log.Printf("kiwi connection closed (%s)", err.Error())
				break
			}
		}

		for _, channel := range channels {
			channel.Client.StartShutdown("client_closed")
		}
	}()
}
