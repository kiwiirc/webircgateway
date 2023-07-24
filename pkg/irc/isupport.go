package irc

import (
	"strings"
	"sync"
)

type ISupport struct {
	Received    bool
	Injected    bool
	Tags        map[string]string
	tokens      map[string]string
	tokensMutex sync.RWMutex
}

func (m *ISupport) ClearTokens() {
	m.tokensMutex.Lock()
	m.tokens = make(map[string]string)
	m.tokensMutex.Unlock()
}

func (m *ISupport) AddToken(tokenPair string) {
	m.tokensMutex.Lock()
	m.addToken(tokenPair)
	m.tokensMutex.Unlock()
}

func (m *ISupport) AddTokens(tokenPairs []string) {
	m.tokensMutex.Lock()
	for _, tp := range tokenPairs {
		m.addToken(tp)
	}
	m.tokensMutex.Unlock()
}

func (m *ISupport) HasToken(key string) (ok bool) {
	m.tokensMutex.RLock()
	_, ok = m.tokens[strings.ToUpper(key)]
	m.tokensMutex.RUnlock()
	return
}

func (m *ISupport) GetToken(key string) (val string) {
	m.tokensMutex.RLock()
	val = m.tokens[strings.ToUpper(key)]
	m.tokensMutex.RUnlock()
	return
}

func (m *ISupport) addToken(tokenPair string) {
	kv := strings.Split(tokenPair, "=")
	if len(kv) == 1 {
		kv = append(kv, "")
	}
	m.tokens[strings.ToUpper(kv[0])] = kv[1]
}
