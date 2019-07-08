package webircgateway

import (
	"strings"

	"github.com/igm/sockjs-go/sockjs"
)

type TransportSockjs struct {
	gateway *Gateway
}

func (t *TransportSockjs) Init(g *Gateway) {
	t.gateway = g
	sockjsHandler := sockjs.NewHandler("/webirc/sockjs", sockjs.DefaultOptions, t.sessionHandler)
	t.gateway.HttpRouter.Handle("/webirc/sockjs/", sockjsHandler)
}

func (t *TransportSockjs) sessionHandler(session sockjs.Session) {
	connInfo := NewClientConnectionInfo(
		strings.ToLower(session.Request().Header.Get("Origin")),
		t.gateway.GetRemoteAddressFromRequest(session.Request()).String(),
		session.Request(),
		t.gateway,
	)

	client, err := t.gateway.NewClient(connInfo)
	if err != nil {
		session.Close(0, err.Error())
		return
	}

	client.Log(2, "New sockjs client on %s from %s %s", session.Request().Host, client.RemoteAddr, client.RemoteHostname)

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
