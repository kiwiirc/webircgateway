package irc

import "strings"

type State struct {
	LocalPort  int
	RemotePort int
	Username   string
	Nick       string
	RealName   string
	Password   string
	Channels   StringMap
}

func NewState() *State {
	return &State{
		Channels: newStringMap(),
	}
}

type StringMap struct {
	m map[string]bool
}

func newStringMap() StringMap {
	return StringMap{m: make(map[string]bool)}
}

func (m StringMap) Set(s string) {
	m.m[strings.ToLower(s)] = true
}

func (m StringMap) Get(s string) (b, ok bool) {
	b, ok = m.m[strings.ToLower(s)]
	return
}

func (m StringMap) Del(s string) {
	delete(m.m, strings.ToLower(s))
}

func (m StringMap) Has(s string) (ok bool) {
	_, ok = m.m[strings.ToLower(s)]
	return
}
