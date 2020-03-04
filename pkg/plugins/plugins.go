package plugins

import (
	"sync"

	"github.com/kiwiirc/webircgateway/pkg/webircgateway"
)

func LoadInternalPlugins(gateway *webircgateway.Gateway, pluginsQuit *sync.WaitGroup) {
	// No internal plugins to load
}

func arrayContains(a []string, v string) bool {
	for _, s := range a {
		if s == v {
			return true
		}
	}
	return false
}
