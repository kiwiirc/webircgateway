package main

import (
	"github.com/kiwiirc/webircgateway/pkg/webircgateway"
)

func Start(gateway *webircgateway.Gateway) {
	gateway.Log(1, "Example gateway plugin %s", webircgateway.Version)
}
