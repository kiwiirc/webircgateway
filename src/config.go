package main

import (
	"log"
	"strings"

	"gopkg.in/ini.v1"
)

// ConfigUpstream - An upstream config
type ConfigUpstream struct {
	Hostname       string
	Port           int
	TLS            bool
	Timeout        int
	Throttle       int
	WebircPassword string
}

type ConfigServer struct {
	LocalAddr string
	Port      int
	TLS       bool
}

// Config - Config options for the running app
var Config struct {
	configFile     string
	logLevel       int
	upstreams      []ConfigUpstream
	servers        []ConfigServer
	serverEngines  []string
	clientRealname string
	clientUsername string
}

func loadConfig() error {
	cfg, err := ini.LoadSources(ini.LoadOptions{AllowBooleanKeys: true}, Config.configFile)
	if err != nil {
		return err
	}

	// Clear the existing config
	Config.upstreams = []ConfigUpstream{}
	Config.servers = []ConfigServer{}
	Config.serverEngines = []string{}

	for _, section := range cfg.Sections() {
		if strings.Index(section.Name(), "DEFAULT") == 0 {
			Config.logLevel = section.Key("logLevel").MustInt(3)
			if Config.logLevel < 1 || Config.logLevel > 3 {
				log.Println("Config option logLevel must be between 1-3. Setting default value of 3.")
				Config.logLevel = 3
			}
		}

		if strings.Index(section.Name(), "clients") == 0 {
			Config.clientUsername = section.Key("username").MustString("")
			Config.clientRealname = section.Key("realname").MustString("")
		}

		if strings.Index(section.Name(), "server.") == 0 {
			server := ConfigServer{}
			server.LocalAddr = section.Key("bind").MustString("127.0.0.1")
			server.Port = section.Key("port").MustInt(80)
			server.TLS = section.Key("tls").MustBool(false)

			Config.servers = append(Config.servers, server)
		}

		if strings.Index(section.Name(), "upstream.") == 0 {
			upstream := ConfigUpstream{}
			upstream.Hostname = section.Key("hostname").MustString("127.0.0.1")
			upstream.Port = section.Key("port").MustInt(6667)
			upstream.TLS = section.Key("tls").MustBool(false)
			upstream.Timeout = section.Key("timeout").MustInt(10)
			upstream.Throttle = section.Key("throttle").MustInt(2)
			upstream.WebircPassword = section.Key("webirc").MustString("")

			Config.upstreams = append(Config.upstreams, upstream)
		}

		if strings.Index(section.Name(), "engines") == 0 {
			for _, engine := range section.KeyStrings() {
				Config.serverEngines = append(Config.serverEngines, strings.Trim(engine, "\n"))
			}
		}
	}

	return nil
}
