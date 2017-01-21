package main

import (
	"net"
	"net/http"
	"strconv"

	"golang.org/x/net/websocket"
)

func websocketHTTPHandler() {
	http.Handle("/", websocket.Handler(websocketHandler))
}

func websocketHandler(ws *websocket.Conn) {
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

	client.Log(2, "New client from %s %s", client.remoteAddr, client.remoteHostname)

	// Read from websocket
	go func() {
		for {
			r := make([]byte, 1024)
			len, err := ws.Read(r)
			if err == nil && len > 0 {
				client.Log(1, "client->: %s", string(r))
				select {
				case client.Recv <- string(r):
				default:
					client.Log(3, "Recv queue full. Dropping data")
					// TODO: Should this really just drop the data or close the connection?
				}

			} else if err != nil {
				client.Log(1, "Websocket read error: %s", err.Error())
				break

			} else if len == 0 {
				client.Log(1, "Got 0 bytes from websocket")
			}
		}

		client.signalClose <- "client_closed"
	}()

	// Write to websocket
	go func() {
		for {
			line, ok := <-client.Send
			if !ok {
				break
			}

			client.Log(1, "->ws: %s", line)
			ws.Write([]byte(line))
		}
	}()

	client.Handle()
}
