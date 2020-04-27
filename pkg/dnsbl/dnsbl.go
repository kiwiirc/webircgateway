package dnsbl

import (
	"encoding/hex"
	"fmt"
	"net"
	"strings"
)

type ResultList struct {
	Listed  bool
	Results []Result
}

/*
Result holds the individual IP lookup results for each RBL search
*/
type Result struct {
	// Blacklist is the DNSBL server that gave this result
	Blacklist string
	// Address is the IP address that was searched
	Address string
	// Listed indicates whether or not the IP was on the RBL
	Listed bool
	// RBL lists sometimes add extra information as a TXT record
	// if any info is present, it will be stored here.
	Text string
	// Error represents any error that was encountered (DNS timeout, host not
	// found, etc.) if any
	Error bool
	// ErrorType is the type of error encountered if any
	ErrorType error
}

/*
Convert an IP to a hostname ready for a dnsbl lookup
127.0.0.1 becomes 1.0.0.127
1234:1234:1234:1234:1234:1234:1234:1234 becomes 4.3.2.1.4.3.2.1.4.3.2.1.4.3.2.1.4.3.2.1.4.3.2.1.4.3.2.1.4.3.2.1
*/
func toDnsBlHostname(ip net.IP) string {
	if ip.To4() != nil {
		// IPv4
		// Reverse the complete octects
		splitAddress := strings.Split(ip.String(), ".")
		for i, j := 0, len(splitAddress)-1; i < len(splitAddress)/2; i, j = i+1, j-1 {
			splitAddress[i], splitAddress[j] = splitAddress[j], splitAddress[i]
		}

		return strings.Join(splitAddress, ".")
	}

	// IPv6
	// Remove all : from a full expanded address, then reverse all the hex characters
	ipv6Str := expandIPv6(ip)
	addrHexStr := strings.ReplaceAll(ipv6Str, ":", "")
	chars := []rune(addrHexStr)
	for i, j := 0, len(chars)-1; i < j; i, j = i+1, j-1 {
		chars[i], chars[j] = chars[j], chars[i]
	}

	return strings.Join(strings.Split(string(chars), ""), ".")
}

func expandIPv6(ip net.IP) string {
	dst := make([]byte, hex.EncodedLen(len(ip)))
	_ = hex.Encode(dst, ip)
	return string(dst[0:4]) + ":" +
		string(dst[4:8]) + ":" +
		string(dst[8:12]) + ":" +
		string(dst[12:16]) + ":" +
		string(dst[16:20]) + ":" +
		string(dst[20:24]) + ":" +
		string(dst[24:28]) + ":" +
		string(dst[28:])
}

func query(rbl string, host string, r *Result) {
	r.Listed = false

	lookup := fmt.Sprintf("%s.%s", host, rbl)
	res, err := net.LookupHost(lookup)

	if len(res) > 0 {
		r.Listed = true
		txt, _ := net.LookupTXT(lookup)
		if len(txt) > 0 {
			r.Text = txt[0]
		}
	}
	if err != nil {
		r.Error = true
		r.ErrorType = err
	}

	return
}

func Lookup(dnsblList []string, targetHost string) (r ResultList) {
	ip, err := net.LookupIP(targetHost)
	if err != nil {
		return
	}

	for _, dnsbl := range dnsblList {
		for _, addr := range ip {
			res := Result{}
			res.Blacklist = dnsbl
			res.Address = addr.String()

			addr := toDnsBlHostname(addr)
			query(dnsbl, addr, &res)
			r.Results = append(r.Results, res)

			if res.Listed {
				r.Listed = true
			}
		}
	}

	return
}
