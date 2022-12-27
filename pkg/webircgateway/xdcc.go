package webircgateway

import (
	"context"
	"log"
	"os"
	"github.com/gorilla/mux"
	  "golang.org/x/exp/maps"
	 "github.com/gosimple/slug"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"regexp"
	"bytes"
	
	"github.com/kiwiirc/webircgateway/pkg/irc"
	
)

func remove[T comparable](l []T, item T) []T{
	for i, other := range l{
		if other == item{
			return append(l[:i],l[i+1:]...)
		}
	}
	return l
}

// Server muxer, dynamic map of handlers, and listen port.
type Server struct {
	Dispatcher *mux.Router
	fileNames  map[string]ParsedParts
	clientsMap map[string][]string
	Port       string
	server     http.Server
}
type XDCCConfig struct {
	Port     string
	DomainName string
	LetsEncryptCacheDir string
	CertFile string
KeyFile string
server Server
TLS bool
}
var Configs = XDCCConfig{
Port :"3000",
DomainName : func(n string, _ error) string { return n }(os.Hostname()),
LetsEncryptCacheDir : "",
CertFile: "",
KeyFile: "",
server: Server{Port: "3000", Dispatcher: mux.NewRouter(), fileNames: make(map[string]ParsedParts),clientsMap: make(map[string][]string), server: http.Server{
	Addr: "3000",
	
}} ,
TLS: false,
}


func int2ip(nn uint32) net.IP {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, nn)
	return ip
}

type ParsedParts struct {
	ip     net.IP
	file   string
	port   int
	length uint64
	receiverNick string
	senderNick string
	serverHostname string

}

func parseSendParams(text string) *ParsedParts {
	re := regexp.MustCompile(`(?:[^\s"]+|"[^"]*")+`)
	replace := regexp.MustCompile(`^"(.+)"$`)

	parts := re.FindAllString(text, -1)

	ipInt, _ := strconv.ParseUint(parts[3], 10, 32)
	portInt, _ := strconv.ParseInt(parts[4], 10, 0)
	lengthInt, _ := strconv.ParseUint(parts[5], 10, 64)
	partsStruct := &ParsedParts{
		file:   replace.ReplaceAllString(parts[2], "$1"),
		ip:     int2ip(uint32(ipInt)),
		port:   int(portInt),
		length: lengthInt,
	}

	return partsStruct

}


type WriteCounter struct {
	Total uint64
	connection *net.Conn
	expectedLength uint64
	writer *io.PipeWriter
}

func (wc *WriteCounter) Write(p []byte) (int, error) {
	n := len(p)
	wc.Total += uint64(n)
	buf := bytes.NewBuffer(make([]byte,8))

	if wc.expectedLength > 0xffffffff {
		binary.Write((*wc.connection), binary.BigEndian, buf.Bytes())	

	}else{
	binary.Write((*wc.connection), binary.BigEndian, buf.Bytes()[4:8])

	}
	if wc.expectedLength == wc.Total{
		(*wc.writer).Close()
	}
	return n, nil
}


func serveFile(parts ParsedParts, w http.ResponseWriter, r *http.Request) (work bool) {

	ipPort := fmt.Sprintf("%s:%d", parts.ip.String(), parts.port)
	//println(strings.Trim(m.GetParamU(1,""),"\x01"))
	//println(parts.ip.String())
	//	println(parts.port)
	if parts.ip == nil {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("404 - You tried"))
		return false
	}
	conn, err := net.Dial("tcp", ipPort)

	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(err.Error()))
		return false
	}

	pr, pw := io.Pipe()
	counter := &WriteCounter{
		connection :&conn,
		Total: 0,
		expectedLength: parts.length,
		writer: pw,
	}


	contentDisposition := fmt.Sprintf("attachment; filename=%s", parts.file)
	w.Header().Set("Content-Disposition", contentDisposition)
	w.Header().Set("Content-Type", "application/octet-stream")
	intLength := int(parts.length)
	if uint64(intLength) != parts.length {
		panic("overflows!")
	}
	w.Header().Set("Content-Length", strconv.Itoa(intLength) /*r.Header.Get("Content-Length")*/)

	go io.Copy(pw, io.TeeReader( conn,w))
	io.Copy(counter, pr)
	
	defer conn.Close()

	
	return true

	
}
func DCCSend(hook *HookIrcLine) {

	if hook.Halt || hook.ToServer {
		return
	}
	client := hook.Client

	data := hook.Line

	if data == "" {
		return
	}

	data = ensureUtf8(data, client.Encoding)
	if data == "" {
		return
	}
	m, parseErr := irc.ParseLine(data)
	if parseErr != nil {
		return
	}

	pLen := len(m.Params)
	

	if pLen > 0 && m.Command == "PRIVMSG" && strings.HasPrefix(strings.Trim(m.GetParamU(1, ""), "\x01"), "DCC SEND") { //can be moved to plugin goto hook.dispatch("irc.line")

		parts := parseSendParams(strings.Trim(m.GetParamU(1, ""), "\x01"))
		parts.receiverNick = client.IrcState.Nick
		parts.senderNick = m.Prefix.Nick
		parts.serverHostname = client.UpstreamConfig.Hostname
		lastIndex := strings.LastIndex(parts.file,".")
		parts.file = strings.ToLower(slug.Make(parts.receiverNick  + strings.ReplaceAll(parts.serverHostname, ".", "_") + parts.senderNick + parts.file[0:lastIndex]) + parts.file[lastIndex:len(parts.file)]) //long URLs may not work
	    hook.Message.Command = "NOTICE"
		hook.Message.Params[1] = fmt.Sprintf("http://%s:3000/%s",Configs.DomainName, parts.file)
		
		_, ok := Configs.server.fileNames[parts.file]
		if ok{
			client.SendClientSignal("data", hook.Message.ToLine())

			return
		}
		
		Configs.server.AddFile(parts.file, *parts)
		log.Printf(parts.file)
		
		client.SendClientSignal("data", hook.Message.ToLine())
	}

}

func DCCClose(hook *HookGatewayClosing) {

	Configs.server.server.Shutdown(context.Background())

}
func ClientClose(hook *HookClientState){
	if !hook.Connected{
		oldKeys := maps.Keys(Configs.server.clientsMap)

    for i := range oldKeys {
        if strings.HasPrefix(oldKeys[i],hook.Client.IrcState.Nick + strings.ReplaceAll(hook.Client.UpstreamConfig.Hostname, ".", "_")) {
			delete(Configs.server.clientsMap,oldKeys[i] )
		}
    }

		
	}

}




func (s *Server) Start() {

	http.ListenAndServe(":"+s.Port, s.Dispatcher)
}

// InitDispatch routes.
func (s *Server) InitDispatch() {
	d := s.Dispatcher



	d.HandleFunc("/{name}", func(w http.ResponseWriter, r *http.Request) {
		//Lookup handler in map and call it, proxying this writer and request
		vars := mux.Vars(r)
		name := vars["name"]

		// s.ProxyCall(w, r, name)

		parts := s.fileNames[name]

		//call serveFile here
		serveFile(parts, w, r) //removed go keyword this could mean servFile can only happen once

		//destroy route
		s.Destroy(parts) 

	}).Methods("GET")
}

func (s *Server) Destroy(parts ParsedParts) {
	delete(s.fileNames, parts.file) 
	s.clientsMap[parts.receiverNick+ strings.ReplaceAll(parts.serverHostname, ".", "_")+parts.senderNick] = remove(s.clientsMap[parts.receiverNick+ strings.ReplaceAll(parts.serverHostname, ".", "_")+parts.senderNick],parts.file)
}



func (s *Server) AddFile( /*w http.ResponseWriter, r *http.Request,*/ fName string, parts ParsedParts) { // add only 1 function instead
	
	//store the parts and the hook
	s.fileNames[fName] = parts // Add the handler to our map

	Configs.server.clientsMap[parts.receiverNick  +  strings.ReplaceAll(parts.serverHostname, ".", "_") + parts.senderNick] = append(Configs.server.clientsMap[parts.receiverNick  + strings.ReplaceAll(parts.serverHostname, ".", "_")+ parts.senderNick],fName)


}
