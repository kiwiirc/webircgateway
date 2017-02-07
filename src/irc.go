package main

import (
	"errors"
	"strings"
)

type IrcMask struct {
	Nick     string
	Username string
	Hostname string
	Mask     string
}
type IrcMessage struct {
	Raw     string
	Tags    map[string]string
	Prefix  *IrcMask
	Command string
	Params  []string
}

type IrcState struct {
	LocalPort  int
	RemotePort int
	Username   string
}

func ircCreateMask(maskStr string) *IrcMask {
	mask := &IrcMask{
		Mask: maskStr,
	}

	usernameStart := strings.Index(maskStr, "!")
	hostStart := strings.Index(maskStr, "@")

	if usernameStart == -1 && hostStart == -1 {
		mask.Nick = maskStr
	} else if usernameStart > -1 && hostStart > -1 {
		mask.Nick = maskStr[0:usernameStart]
		mask.Username = maskStr[usernameStart+1 : hostStart]
		mask.Hostname = maskStr[hostStart+1:]
	} else if usernameStart > -1 && hostStart == -1 {
		mask.Nick = maskStr[0:usernameStart]
		mask.Username = maskStr[usernameStart+1:]
	} else if usernameStart == -1 && hostStart > -1 {
		mask.Username = maskStr[0:hostStart]
		mask.Hostname = maskStr[hostStart+1:]
	}

	return mask
}

// ircParseLine - Turn a raw IRC line into a message
func ircParseLine(line string) (*IrcMessage, error) {
	message := &IrcMessage{
		Raw:  line,
		Tags: make(map[string]string),
	}

	token := ""
	rest := ""

	token, rest = ircNextToken(line, false)
	if token == "" {
		return nil, errors.New("Empty line")
	}

	// Tags. Starts with "@"
	if token[0] == 64 {
		tagsRaw := token[1:]
		tags := strings.Split(tagsRaw, ";")
		for _, tag := range tags {
			parts := strings.Split(tag, "=")
			if len(parts) == 1 {
				message.Tags[parts[0]] = ""
			} else {
				message.Tags[parts[0]] = parts[1]
			}
		}

		token, rest = ircNextToken(rest, false)
	}

	// Prefix. Starts with ":"
	if token != "" && token[0] == 58 {
		message.Prefix = ircCreateMask(token[1:])
		token, rest = ircNextToken(rest, false)
	}

	// Command
	if token == "" {
		return nil, errors.New("Missing command")
	}

	message.Command = token

	// Params
	for {
		token, rest = ircNextToken(rest, true)
		if token == "" {
			break
		}

		message.Params = append(message.Params, token)
	}

	return message, nil
}

func ircNextToken(s string, allowTrailing bool) (string, string) {
	s = strings.TrimLeft(s, " ")

	if len(s) == 0 {
		return "", ""
	}

	// The last token (trailing) start with :
	if allowTrailing && s[0] == 58 {
		return s[1:], ""
	}

	token := ""
	spaceIdx := strings.Index(s, " ")
	if spaceIdx > -1 {
		token = s[:spaceIdx]
		s = s[spaceIdx+1:]
	} else {
		token = s
		s = ""
	}

	return token, s
}
