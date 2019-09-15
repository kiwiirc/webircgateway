package webircgateway

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"golang.org/x/net/websocket"
)

type TransportWebsocket struct {
	gateway  *Gateway
	wsServer *websocket.Server
}

func (t *TransportWebsocket) Init(g *Gateway) {
	t.gateway = g
	t.wsServer = &websocket.Server{Handler: t.websocketHandler, Handshake: t.checkOrigin}
	t.gateway.HttpRouter.Handle("/webirc/websocket/", t.wsServer)
}

func (t *TransportWebsocket) checkOrigin(config *websocket.Config, req *http.Request) (err error) {
	config.Origin, err = websocket.Origin(config, req)

	var origin string
	if config.Origin != nil {
		origin = config.Origin.String()
	} else {
		origin = ""
	}

	if !t.gateway.isClientOriginAllowed(origin) {
		err = fmt.Errorf("Origin %#v not allowed", origin)
		t.gateway.Log(2, "%s. Closing connection", err)
		return err
	}

	return err
}

func (t *TransportWebsocket) websocketHandler(ws *websocket.Conn) {
	req := ws.Request()
	gateway := t.gateway
	originURL, originParseErr := websocket.Origin(&t.wsServer.Config, req)
	if originParseErr != nil {
		err := fmt.Errorf("Invalid origin: %s", originParseErr)
		t.gateway.Log(4, "%s", err)
		return
	}
	origin := originURL.String()
	remoteAddr := t.gateway.GetRemoteAddressFromRequest(ws.Request()).String()

	connInfo := NewClientConnectionInfo(origin, remoteAddr, req, gateway)

	client, err := t.gateway.NewClient(connInfo)
	if err != nil {
		ws.Close()
		return
	}

	client.Log(2, "New websocket client on %s from %s %s", ws.Request().Host, client.RemoteAddr, client.RemoteHostname)

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
