package webircgateway

import (
	"net"
	"net/http"
	"strings"
	"sync"

	"golang.org/x/net/websocket"
)

func websocketHTTPHandler(router *http.ServeMux) {
	router.Handle("/webirc/websocket/", websocket.Handler(websocketHandler))
}

func websocketHandler(ws *websocket.Conn) {
	client := NewClient()

	originHeader := strings.ToLower(ws.Request().Header.Get("Origin"))
	if !isClientOriginAllowed(originHeader) {
		client.Log(2, "Origin %s not allowed. Closing connection", originHeader)
		ws.Close()
		return
	}

	client.RemoteAddr = GetRemoteAddressFromRequest(ws.Request()).String()

	clientHostnames, err := net.LookupAddr(client.RemoteAddr)
	if err != nil {
		client.RemoteHostname = client.RemoteAddr
	} else {
		// FQDNs include a . at the end. Strip it out
		client.RemoteHostname = strings.Trim(clientHostnames[0], ".")
	}

	client.Log(2, "New client from %s %s", client.RemoteAddr, client.RemoteHostname)

	// We wait until the client send queue has been drained
	var sendDrained sync.WaitGroup
	sendDrained.Add(1)

	// Read from websocket
	go func() {
		for {
			r := make([]byte, 1024)
			len, err := ws.Read(r)
			if err == nil && len > 0 {
				message := string(r[:len])
				client.Log(1, "client->: %s", message)
				select {
				case client.Recv <- message:
				default:
					client.Log(3, "Recv queue full. Dropping data")
					// TODO: Should this really just drop the data or close the connection?
				}

			} else if err != nil {
				client.Log(1, "Websocket connection closed (%s)", err.Error())
				break

			} else if len == 0 {
				client.Log(1, "Got 0 bytes from websocket")
			}
		}

		close(client.Recv)
		client.StartShutdown("client_closed")
	}()

	// Process signals for the client
	for {
		signal, ok := <-client.Signals
		if !ok {
			sendDrained.Done()
			break
		}

		if signal[0] == "data" {
			line := strings.Trim(signal[1], "\r\n")
			client.Log(1, "->ws: %s", line)
			ws.Write([]byte(line))
		}
	}

	sendDrained.Wait()
	ws.Close()
}
