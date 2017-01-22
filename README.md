# websocketgateway
Simple websocket gateway to IRC networks for web clients

### Overview
* Multiple servers, non-tls / tls / multiple ports
* Multiple websocket engine support
    * Websockets
    * SockJS
* Multiple upstream IRC servers in a round robin fashion
* WEBIRC support
* Static username and realname values
* Hexed IP in the username and realname fields

### Running
Once compiled and you have a config file set, run `./websocketgateway --config=config.conf` to start the gateway server. You may reload the configuration file without any downtime by sending SIGHUP to the process.

### TODO
Several TODO items are added to the Github issue tracker.
* Pre-built binaries
* Documentation on how best to distribute and handle configuration files
