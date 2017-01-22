#!/bin/sh

# Helper to run and build webircgateway

case "$1" in
        prepare)
                echo Downloading dependency: github.com/igm/sockjs-go/sockjs
                go get github.com/igm/sockjs-go/sockjs
                echo Downloading dependency: gopkg.in/ini.v1
                go get gopkg.in/ini.v1
                echo Downloading dependency: golang.org/x/net/websocket
                go get golang.org/x/net/websocket
                echo Complete!
                ;;

        build)
                echo Building webircgateway..
                rm -f ./webircgateway
                go build -o webircgateway src/*.go
                chmod +x ./webircgateway
                ls -lh ./webircgateway
                ;;

        run)
                go run src/*.go "${@:2}"
                ;;

        *)
                echo "- webircgateway helper"
                echo "- This project is built using golang. Make sure to have it installed and configured first!"
                echo "$0 prepare - Download and prepare any dependencies "
                echo "$0 build - Build webircgateway "
                echo "$0 run   - Run webircgateway from sources "
esac
