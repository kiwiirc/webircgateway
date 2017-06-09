#!/bin/bash

# Helper to run and build webircgateway

ROOTPATH=$( cd $(dirname $0) ; pwd )

case "$1" in
        prepare)
                echo Downloading dependency: github.com/igm/sockjs-go/sockjs
                go get github.com/igm/sockjs-go/sockjs
                echo Downloading dependency: gopkg.in/ini.v1
                go get gopkg.in/ini.v1
                echo Downloading dependency: golang.org/x/net/websocket
                go get golang.org/x/net/websocket
                echo Downloading dependency: github.com/gobwas/glob
                go get github.com/gobwas/glob
                echo Downloading dependency: rsc.io/letsencrypt
                go get rsc.io/letsencrypt
                echo Downloading dependency: github.com/orcaman/concurrent-map
                go get github.com/orcaman/concurrent-map
                echo Complete!
                ;;

        build)
                OUTFILE=${2:-webircgateway}
                echo Building $OUTFILE
                rm -f $OUTFILE
                go build -o $OUTFILE $ROOTPATH/main.go
                chmod +x $OUTFILE
                ls -lh $OUTFILE
                ;;

        run)
                go run $ROOTPATH/main.go "${@:2}"
                ;;

        *)
                echo "- webircgateway helper"
                echo "- This project is built using golang. Make sure to have it installed and configured first!"
                echo "$0 prepare - Download and prepare any dependencies "
                echo "$0 build - Build webircgateway "
                echo "$0 run   - Run webircgateway from sources "
esac
