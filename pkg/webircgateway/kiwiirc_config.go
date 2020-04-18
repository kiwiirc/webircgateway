package webircgateway

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"sync"

	"github.com/flynn/json5"
)

var kiwiircConfig []byte
var kiwiircConfigLock sync.RWMutex

func (s *Gateway) LoadKiwiircConfig() error {
	configPath := s.Config.ResolvePath(s.Config.KiwiircConfig)
	file, err := ioutil.ReadFile(configPath)
	if err != nil {
		return err
	}
	config := make(map[string]interface{})

	if err = json5.Unmarshal([]byte(file), &config); err != nil {
		return err
	}
	config["kiwiServer"] = "/webirc/kiwiirc/"

	kiwiircConfigLock.Lock()
	if kiwiircConfig, err = json.Marshal(config); err != nil {
		kiwiircConfigLock.Unlock()
		return err
	}
	kiwiircConfigLock.Unlock()

	return nil
}

func sendKiwiircConfig(w http.ResponseWriter, r *http.Request) {
	kiwiircConfigLock.RLock()
	defer kiwiircConfigLock.RUnlock()
	w.Write(kiwiircConfig)
}
