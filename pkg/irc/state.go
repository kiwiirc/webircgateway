package irc

import (
	"strings"
	"sync"
	"time"
)

type State struct {
	LocalPort  int
	RemotePort int
	Username   string
	Nick       string
	RealName   string
	Password   string
	Account    string
	Modes      map[string]string

	channelsMutex sync.Mutex
	Channels      map[string]*StateChannel
	ISupport      *ISupport
}

type StateChannel struct {
	Name   string
	Modes  map[string]string
	Joined time.Time
}

func NewState() *State {
	return &State{
		Channels: make(map[string]*StateChannel),
		ISupport: &ISupport{
			tokens: make(map[string]string),
		},
	}
}

func NewStateChannel(name string) *StateChannel {
	return &StateChannel{
		Name:   name,
		Modes:  make(map[string]string),
		Joined: time.Now(),
	}
}

func (m *State) HasChannel(name string) (ok bool) {
	m.channelsMutex.Lock()
	_, ok = m.Channels[strings.ToLower(name)]
	m.channelsMutex.Unlock()
	return
}

func (m *State) GetChannel(name string) (channel *StateChannel) {
	m.channelsMutex.Lock()
	channel = m.Channels[strings.ToLower(name)]
	m.channelsMutex.Unlock()
	return
}

func (m *State) SetChannel(channel *StateChannel) {
	m.channelsMutex.Lock()
	m.Channels[strings.ToLower(channel.Name)] = channel
	m.channelsMutex.Unlock()
}

func (m *State) RemoveChannel(name string) {
	m.channelsMutex.Lock()
	delete(m.Channels, strings.ToLower(name))
	m.channelsMutex.Unlock()
}

func (m *State) ClearChannels() {
	m.channelsMutex.Lock()
	for i := range m.Channels {
		delete(m.Channels, i)
	}
	m.channelsMutex.Unlock()
}
