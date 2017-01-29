# webircgateway
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

### Building and development
webircgateway is built using golang - make sure to have this installed and configured first!

Included is a `webircgateway.sh` helper file to wrap building and running the project during development.
* `./webircgateway.sh prepare` will download any dependencies the project has
* `./webircgateway.sh build` will build the project to the `./webircgateway` binary
* `./webircgateway.sh run` will run the project from sources

### Running
Once compiled and you have a config file set, run `./websocketgateway --config=config.conf` to start the gateway server. You may reload the configuration file without restarting the server (no downtime!) by sending SIGHUP to the process, `kill -1 <pid of webircgateway>`.

### TODO
Several TODO items are added to the Github issue tracker.
* Pre-built binaries
* Documentation on how best to distribute and handle configuration files

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
