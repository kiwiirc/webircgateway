package webircgateway

import (
	"errors"
	"math/rand"
	"net"
	"net/http"
	"strings"
)

var v4LoopbackAddr = net.ParseIP("127.0.0.1")

func (s *Gateway) NewClient() *Client {
	return NewClient(s)
}

func (s *Gateway) IsClientOriginAllowed(originHeader string) bool {
	// Empty list of origins = all origins allowed
	if len(s.Config.RemoteOrigins) == 0 {
		return true
	}

	// No origin header = running on the same page
	if originHeader == "" {
		return true
	}

	foundMatch := false

	for _, originMatch := range s.Config.RemoteOrigins {
		if originMatch.Match(originHeader) {
			foundMatch = true
			break
		}
	}

	return foundMatch
}

func (s *Gateway) isIrcAddressAllowed(addr string) bool {
	// Empty whitelist = all destinations allowed
	if len(s.Config.GatewayWhitelist) == 0 {
		return true
	}

	foundMatch := false

	for _, addrMatch := range s.Config.GatewayWhitelist {
		if addrMatch.Match(addr) {
			foundMatch = true
			break
		}
	}

	return foundMatch
}

func (s *Gateway) findUpstream() (ConfigUpstream, error) {
	var ret ConfigUpstream

	if len(s.Config.Upstreams) == 0 {
		return ret, errors.New("No upstreams available")
	}

	randIdx := rand.Intn(len(s.Config.Upstreams))
	ret = s.Config.Upstreams[randIdx]

	return ret, nil
}

func (s *Gateway) findWebircPassword(ircHost string) string {
	pass, exists := s.Config.GatewayWebircPassword[strings.ToLower(ircHost)]
	if !exists {
		pass = ""
	}

	return pass
}

func (s *Gateway) GetRemoteAddressFromRequest(req *http.Request) net.IP {
	remoteIP := remoteIPFromRequest(req)

	// If the remoteIP is not in a whitelisted reverse proxy range, don't trust
	// the headers and use the remoteIP as the users IP
	if !s.isTrustedProxy(remoteIP) {
		return remoteIP
	}

	headerVal := req.Header.Get("x-forwarded-for")
	ips := strings.Split(headerVal, ",")
	ipStr := strings.Trim(ips[0], " ")
	if ipStr != "" {
		ip := net.ParseIP(ipStr)
		if ip != nil {
			remoteIP = ip
		}
	}

	return remoteIP

}

func (s *Gateway) isRequestSecure(req *http.Request) bool {
	remoteIP := remoteIPFromRequest(req)

	// If the remoteIP is not in a whitelisted reverse proxy range, don't trust
	// the headers and check the request directly
	if !s.isTrustedProxy(remoteIP) {
		return req.TLS != nil
	}

	fwdProto := req.Header.Get("x-forwarded-proto")
	return strings.EqualFold(fwdProto, "https")
}

func (s *Gateway) isTrustedProxy(remoteIP net.IP) bool {
	for _, cidrRange := range s.Config.ReverseProxies {
		if cidrRange.Contains(remoteIP) {
			return true
		}
	}
	return false
}

func remoteIPFromRequest(req *http.Request) net.IP {
	if req.RemoteAddr == "@" {
		// remote address is unix socket, treat it as loopback interface
		return v4LoopbackAddr
	}

	remoteAddr, _, _ := net.SplitHostPort(req.RemoteAddr)
	return net.ParseIP(remoteAddr)
}
