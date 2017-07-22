package webircgateway

import (
	"log"
	"net"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gobwas/glob"
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
	Proxy          *ConfigProxy
}

// ConfigServer - A web server config
type ConfigServer struct {
	LocalAddr            string
	Port                 int
	TLS                  bool
	CertFile             string
	KeyFile              string
	LetsEncryptCacheFile string
}

type ConfigProxy struct {
	Type      string
	Hostname  string
	Port      int
	TLS       bool
	Username  string
	Interface string
}

// Config - Config options for the running app
var Config struct {
	ConfigFile            string
	LogLevel              int
	Gateway               bool
	GatewayWhitelist      []glob.Glob
	GatewayThrottle       int
	GatewayTimeout        int
	GatewayWebircPassword map[string]string
	Upstreams             []ConfigUpstream
	Servers               []ConfigServer
	ServerEngines         []string
	RemoteOrigins         []glob.Glob
	ReverseProxies        []net.IPNet
	Webroot               string
	ClientRealname        string
	ClientUsername        string
	Identd                bool
}

// ConfigResolvePath - If relative, resolve a path to it's full absolute path relative to the config file
func ConfigResolvePath(path string) string {
	// Absolute paths should stay as they are
	if path[0:1] == "/" {
		return path
	}

	resolved := filepath.Dir(Config.ConfigFile)
	resolved = filepath.Clean(resolved + "/" + path)
	return resolved
}

func SetConfigFile(ConfigFile string) {
	// Config paths starting with $ is executed rather than treated as a path
	if strings.HasPrefix(ConfigFile, "$ ") {
		Config.ConfigFile = ConfigFile
	} else {
		Config.ConfigFile, _ = filepath.Abs(ConfigFile)
	}
}

// CurrentConfigFile - Return the full path or command for the config file in use
func CurrentConfigFile() string {
	return Config.ConfigFile
}
func LoadConfig() error {
	var configSrc interface{}

	if strings.HasPrefix(Config.ConfigFile, "$ ") {
		cmdRawOut, err := exec.Command("sh", "-c", Config.ConfigFile[2:]).Output()
		if err != nil {
			return err
		}

		configSrc = cmdRawOut
	} else {
		configSrc = Config.ConfigFile
	}

	cfg, err := ini.LoadSources(ini.LoadOptions{AllowBooleanKeys: true}, configSrc)
	if err != nil {
		return err
	}

	// Clear the existing config
	Config.Gateway = false
	Config.GatewayWebircPassword = make(map[string]string)
	Config.Upstreams = []ConfigUpstream{}
	Config.Servers = []ConfigServer{}
	Config.ServerEngines = []string{}
	Config.RemoteOrigins = []glob.Glob{}
	Config.GatewayWhitelist = []glob.Glob{}
	Config.ReverseProxies = []net.IPNet{}
	Config.Webroot = ""

	for _, section := range cfg.Sections() {
		if strings.Index(section.Name(), "DEFAULT") == 0 {
			Config.LogLevel = section.Key("logLevel").MustInt(3)
			if Config.LogLevel < 1 || Config.LogLevel > 3 {
				log.Println("Config option logLevel must be between 1-3. Setting default value of 3.")
				Config.LogLevel = 3
			}

			Config.Identd = section.Key("identd").MustBool(false)
		}

		if section.Name() == "gateway" {
			Config.Gateway = section.Key("enabled").MustBool(false)
			Config.GatewayTimeout = section.Key("timeout").MustInt(10)
			Config.GatewayThrottle = section.Key("throttle").MustInt(2)
		}

		if section.Name() == "gateway.webirc" {
			for _, serverAddr := range section.KeyStrings() {
				Config.GatewayWebircPassword[serverAddr] = section.Key(serverAddr).MustString("")
			}
		}

		if strings.Index(section.Name(), "clients") == 0 {
			Config.ClientUsername = section.Key("username").MustString("")
			Config.ClientRealname = section.Key("realname").MustString("")
		}

		if strings.Index(section.Name(), "fileserving") == 0 {
			if section.Key("enabled").MustBool(false) {
				Config.Webroot = section.Key("webroot").MustString("")
			}
		}

		if strings.Index(section.Name(), "server.") == 0 {
			server := ConfigServer{}
			server.LocalAddr = section.Key("bind").MustString("127.0.0.1")
			server.Port = section.Key("port").MustInt(80)
			server.TLS = section.Key("tls").MustBool(false)
			server.CertFile = section.Key("cert").MustString("")
			server.KeyFile = section.Key("key").MustString("")
			server.LetsEncryptCacheFile = section.Key("letsencrypt_cache").MustString("")

			Config.Servers = append(Config.Servers, server)
		}

		if strings.Index(section.Name(), "upstream.") == 0 {
			upstream := ConfigUpstream{}
			upstream.Hostname = section.Key("hostname").MustString("127.0.0.1")
			upstream.Port = section.Key("port").MustInt(6667)
			upstream.TLS = section.Key("tls").MustBool(false)
			upstream.Timeout = section.Key("timeout").MustInt(10)
			upstream.Throttle = section.Key("throttle").MustInt(2)
			upstream.WebircPassword = section.Key("webirc").MustString("")

			Config.Upstreams = append(Config.Upstreams, upstream)
		}

		if strings.Index(section.Name(), "engines") == 0 {
			for _, engine := range section.KeyStrings() {
				Config.ServerEngines = append(Config.ServerEngines, strings.Trim(engine, "\n"))
			}
		}

		if strings.Index(section.Name(), "allowed_origins") == 0 {
			for _, origin := range section.KeyStrings() {
				match, err := glob.Compile(origin)
				if err != nil {
					log.Println("Config section allowed_origins has invalid match, " + origin)
					continue
				}
				Config.RemoteOrigins = append(Config.RemoteOrigins, match)
			}
		}

		if strings.Index(section.Name(), "gateway.whitelist") == 0 {
			for _, origin := range section.KeyStrings() {
				match, err := glob.Compile(origin)
				if err != nil {
					log.Println("Config section gateway.whitelist has invalid match, " + origin)
					continue
				}
				Config.GatewayWhitelist = append(Config.GatewayWhitelist, match)
			}
		}

		if strings.Index(section.Name(), "reverse_proxies") == 0 {
			for _, cidrRange := range section.KeyStrings() {
				_, validRange, cidrErr := net.ParseCIDR(cidrRange)
				if cidrErr != nil {
					log.Println("Config section reverse_proxies has invalid entry, " + cidrRange)
					continue
				}
				Config.ReverseProxies = append(Config.ReverseProxies, *validRange)
			}
		}
	}

	return nil
}
