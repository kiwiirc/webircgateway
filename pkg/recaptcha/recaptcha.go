// Google re-captcha package tweaked from http://github.com/haisum/recaptcha

package recaptcha

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/url"
	"sync"
	"time"
)

var cacheVerified sync.Map
var cacheLife, _ = time.ParseDuration("90s")
var cacheFrequency, _ = time.ParseDuration("10m")
var cacheTicker = time.NewTicker(cacheFrequency)

// CacheItem represents an item in response cache
type CacheItem struct {
	created    int64
	remoteAddr string
}

// R type represents an object of Recaptcha and has public property Secret,
// which is secret obtained from google recaptcha tool admin interface
type R struct {
	URL       string
	Secret    string
	lastError []string
}

// Struct for parsing json in google's response
type googleResponse struct {
	Success    bool
	ErrorCodes []string `json:"error-codes"`
}

// Init starts recpatcha response cache cleanup
func Init() {
	go func() {
		for range cacheTicker.C {
			cleanCache()
		}
	}()
}

func cleanCache() {
	cacheVerified.Range(func(key interface{}, value interface{}) bool {
		cacheItem := value.(CacheItem)
		expired := time.Now().Unix() - int64(cacheLife.Seconds())
		if cacheItem.created < expired {
			cacheVerified.Delete(key)
		}
		return true
	})
}

func verifyCached(response string, remoteAddr string) bool {
	cacheInterface, ok := cacheVerified.Load(response)
	if !ok {
		// Not in cache
		return false
	}
	cacheItem := cacheInterface.(CacheItem)
	if cacheItem.remoteAddr != remoteAddr {
		// None matching remoteAddr
		return false
	}
	expired := time.Now().Unix() - int64(cacheLife.Seconds())
	if cacheItem.created < expired {
		// Cached response expired
		cacheVerified.Delete(remoteAddr)
		return false
	}

	// Valid response
	return true
}

// VerifyResponse is a method similar to `Verify`; but doesn't parse the form for you.  Useful if
// you're receiving the data as a JSON object from a javascript app or similar.
func (r *R) VerifyResponse(response string, remoteAddr string) bool {
	if verifyCached(response, remoteAddr) {
		// Response is cached and still valid
		return true
	}
	r.lastError = make([]string, 1)
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.PostForm(r.URL,
		url.Values{"secret": {r.Secret}, "response": {response}})
	if err != nil {
		r.lastError = append(r.lastError, err.Error())
		return false
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		r.lastError = append(r.lastError, err.Error())
		return false
	}
	gr := new(googleResponse)
	err = json.Unmarshal(body, gr)
	if err != nil {
		r.lastError = append(r.lastError, err.Error())
		return false
	}
	if gr.Success {
		cacheVerified.Store(response, CacheItem{
			created:    time.Now().Unix(),
			remoteAddr: remoteAddr,
		})
	} else {
		r.lastError = append(r.lastError, gr.ErrorCodes...)
	}
	return gr.Success
}

// LastError returns errors occurred in last re-captcha validation attempt
func (r R) LastError() []string {
	return r.lastError
}
