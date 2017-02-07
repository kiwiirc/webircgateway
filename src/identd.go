package main

import (
	"fmt"
	"net"
	"net/textproto"
	"sync"
)

var identd IdentdServer

type IdentdServer struct {
	Entries     map[string]string
	EntriesLock sync.Mutex
}

func NewIdentdServer() IdentdServer {
	return IdentdServer{
		Entries: make(map[string]string),
	}
}

func (i *IdentdServer) AddIdent(localPort, remotePort int, ident string) {
	i.EntriesLock.Lock()
	i.Entries[fmt.Sprintf("%d-%d", localPort, remotePort)] = ident
	i.EntriesLock.Unlock()
}
func (i *IdentdServer) RemoveIdent(localPort, remotePort int) {
	i.EntriesLock.Lock()
	delete(i.Entries, fmt.Sprintf("%d-%d", localPort, remotePort))
	i.EntriesLock.Unlock()
}

func (i *IdentdServer) Run() error {
	serv, err := net.Listen("tcp", ":113")
	if err != nil {
		return err
	}

	go i.ListenForRequests(&serv)
	return nil
}

func (i *IdentdServer) ListenForRequests(serverSocket *net.Listener) {
	for {
		serv := *serverSocket
		client, err := serv.Accept()
		if err != nil {
			break
		}

		go func(conn net.Conn) {
			tc := textproto.NewConn(conn)

			line, err := tc.ReadLine()
			if err != nil {
				conn.Close()
				return
			}

			var localPort, remotePort int
			fmt.Sscanf(line, "%d, %d", &localPort, &remotePort)
			if localPort > 0 && remotePort > 0 {
				i.EntriesLock.Lock()
				ident, ok := i.Entries[fmt.Sprintf("%d-%d", localPort, remotePort)]
				i.EntriesLock.Unlock()
				if !ok {
					fmt.Fprintf(conn, "%d, %d : ERROR : NO-USER\r\n", localPort, remotePort)
				} else {
					fmt.Fprintf(conn, "%d, %d : USERID : UNIX : %s\r\n", localPort, remotePort, ident)
				}
			}

			conn.Close()
		}(client)
	}
}
