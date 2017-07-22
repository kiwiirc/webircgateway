package webircgateway

import (
	"net"
	"net/http"
	"strings"

	"github.com/igm/sockjs-go/sockjs"
)

func sockjsHTTPHandler(router *http.ServeMux) {
	sockjsHandler := sockjs.NewHandler("/webirc/sockjs", sockjs.DefaultOptions, sockjsHandler)
	router.Handle("/webirc/sockjs/", sockjsHandler)
}

func sockjsHandler(session sockjs.Session) {
	client := NewClient()

	originHeader := strings.ToLower(session.Request().Header.Get("Origin"))
	if !isClientOriginAllowed(originHeader) {
		client.Log(2, "Origin %s not allowed. Closing connection", originHeader)
		session.Close(0, "Origin not allowed")
		return
	}

	client.RemoteAddr = GetRemoteAddressFromRequest(session.Request()).String()

	clientHostnames, err := net.LookupAddr(client.RemoteAddr)
	if err != nil {
		client.RemoteHostname = client.RemoteAddr
	} else {
		// FQDNs include a . at the end. Strip it out
		client.RemoteHostname = strings.Trim(clientHostnames[0], ".")
	}

	client.Log(2, "New client from %s %s", client.RemoteAddr, client.RemoteHostname)

	// Read from sockjs
	go func() {
		for {
			msg, err := session.Recv()
			if err == nil && len(msg) > 0 {
				client.Log(1, "client->: %s", msg)
				select {
				case client.Recv <- msg:
				default:
					client.Log(3, "Recv queue full. Dropping data")
					// TODO: Should this really just drop the data or close the connection?
				}
			} else if err != nil {
				client.Log(1, "sockjs connection closed (%s)", err.Error())
				break
			} else if len(msg) == 0 {
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
			break
		}

		if signal[0] == "data" {
			line := strings.Trim(signal[1], "\r\n")
			client.Log(1, "->ws: %s", line)
			session.Send(line)
		}
	}
}
