# webircgateway
Simple http/websocket gateway to IRC networks for web clients

*Pre-built binaries can be downloaded from https://kiwiirc.com/downloads/index.html*

### Overview
Most IRC networks currently do not support websocket connections or will only support native websockets. This causes problems:
* Not all browsers support websockets
* Many antivirus and firewall software cuts websocket connections
* Many transparent proxies block websocket connections (corporate proxies, hotel wifi access points, etc)
* Almost half the internet access is now over mobile connections. These are not as stable as landline connections and will cause a increase in ping timeouts on networks (travelling under a tunnel?)

webircgateway aims to solve these problems by supporting different transport engines to increase browser support and improve connectivity. Web IRC clients still talk the native IRC protocol to webircgateay no matter which transport engine they use.

The `kiwiirc` transport has been designed to work with kiwiirc to further increase the user facing experience and support multiple IRC connections over the same connections if applicable. However, other clients may also make use of this transport engine in future.

##### Current featureset
* Multiple servers, non-tls / tls / multiple ports
* Multiple websocket / transport engine support
    * Websockets (/webirc/websocket/)
    * SockJS (/webirc/sockjs/)
    * Kiwi IRC multi-servers (/webirc/kiwi/)
* Multiple upstream IRC servers in a round robin fashion
* Optional support for "HOST irc.net.org:6667" from clients to connect to any IRCd
* WEBIRC support
* Static username and realname values
* Hexed IP in the username and realname fields
* Optional serving of static files over HTTP

### Building and development
webircgateway is built using golang - make sure to have this installed and configured first!

Included is a `webircgateway.sh` helper file to wrap building and running the project during development.
* `./webircgateway.sh prepare` will download any dependencies the project has
* `./webircgateway.sh build` will build the project to the `./webircgateway` binary
* `./webircgateway.sh run` will run the project from sources

### Running
Once compiled and you have a config file set, run `./websocketgateway --config=config.conf` to start the gateway server. You may reload the configuration file without restarting the server (no downtime!) by sending SIGHUP to the process, `kill -1 <pid of webircgateway>`.

### Recommendations
To ensure web clients can connect to your network and to try keep some consistency between networks:

1. Run the websocketgateway server over HTTPS. Without it, clients running on HTTPS may be blocked from connecting by their browser. You can use https://letsencrypt.org/ for a free signed certificate.
2. Stick to the default engine paths (eg. /webirc/websocket/) and standard web ports (80, 443 for HTTPS) so that clients will know where to connect to.
3. Configure WEBIRC for your IRC servers. This will show the users correct hostname on your network so that bans work.
4. Treat IRC connections made from webircgateway the same as any other IRC connection. Ban evasion and other difficulties arise when networks change web users hostnames / idents. If you must, try setting the users realname field instead.
5. If your network uses `irc.network.org`, use `ws.network.org` to point to your webircgateway.
6. Although available, disable identd lookups for webircgateway clients. There are no benefits while potentially slowing the connection down.

### License
~~~
   Copyright 2017 Kiwi IRC

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
~~~
