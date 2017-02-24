#!/bin/sh

# Helper to run and build webircgateway

# We have some internal packages under ./pkg
ROOTPATH=$( cd $(dirname $0) ; pwd )
GOPATH=$ROOTPATH/pkg:$GOPATH

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
                echo Complete!
                ;;

        build)
                echo Building webircgateway..
                OUTFILE=${2:-webircgateway}
                rm -f $OUTFILE
                go build -o $OUTFILE $ROOTPATH/src/*.go
                chmod +x $OUTFILE
                ls -lh $OUTFILE
                ;;

        run)
                go run $ROOTPATH/src/*.go "${@:2}"
                ;;

        *)
                echo "- webircgateway helper"
                echo "- This project is built using golang. Make sure to have it installed and configured first!"
                echo "$0 prepare - Download and prepare any dependencies "
                echo "$0 build - Build webircgateway "
                echo "$0 run   - Run webircgateway from sources "
esac
