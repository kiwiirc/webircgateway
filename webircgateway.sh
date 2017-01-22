#!/bin/sh

# Helper to run and build webircgateway

case "$1" in
        build)
                echo Building webircgateway..
                rm webircgateway
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
                echo "$0 build - Build webircgateway "
                echo "$0 run   - Run webircgateway from sources "
esac
