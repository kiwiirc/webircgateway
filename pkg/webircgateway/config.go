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
	configFile            string
	logLevel              int
	gateway               bool
	gatewayThrottle       int
	gatewayTimeout        int
	gatewayWebircPassword map[string]string
	upstreams             []ConfigUpstream
	servers               []ConfigServer
	serverEngines         []string
	remoteOrigins         []glob.Glob
	remoteDestinations    []glob.Glob
	reverseProxies        []net.IPNet
	webroot               string
	clientRealname        string
	clientUsername        string
	identd                bool
}

// ConfigResolvePath - If relative, resolve a path to it's full absolute path relative to the config file
func ConfigResolvePath(path string) string {
	// Absolute paths should stay as they are
	if path[0:1] == "/" {
		return path
	}

	resolved := filepath.Dir(Config.configFile)
	resolved = filepath.Clean(resolved + "/" + path)
	return resolved
}

func SetConfigFile(configFile string) {
	// Config paths starting with $ is executed rather than treated as a path
	if strings.HasPrefix(configFile, "$ ") {
		Config.configFile = configFile
	} else {
		Config.configFile, _ = filepath.Abs(configFile)
	}
}

// CurrentConfigFile - Return the full path or command for the config file in use
func CurrentConfigFile() string {
	return Config.configFile
}
func LoadConfig() error {
	var configSrc interface{}

	if strings.HasPrefix(Config.configFile, "$ ") {
		cmdRawOut, err := exec.Command("sh", "-c", Config.configFile[2:]).Output()
		if err != nil {
			return err
		}

		configSrc = cmdRawOut
	} else {
		configSrc = Config.configFile
	}

	cfg, err := ini.LoadSources(ini.LoadOptions{AllowBooleanKeys: true}, configSrc)
	if err != nil {
		return err
	}

	// Clear the existing config
	Config.gateway = false
	Config.gatewayWebircPassword = make(map[string]string)
	Config.upstreams = []ConfigUpstream{}
	Config.servers = []ConfigServer{}
	Config.serverEngines = []string{}
	Config.remoteOrigins = []glob.Glob{}
	Config.remoteDestinations = []glob.Glob{}
	Config.reverseProxies = []net.IPNet{}
	Config.webroot = ""

	for _, section := range cfg.Sections() {
		if strings.Index(section.Name(), "DEFAULT") == 0 {
			Config.logLevel = section.Key("logLevel").MustInt(3)
			if Config.logLevel < 1 || Config.logLevel > 3 {
				log.Println("Config option logLevel must be between 1-3. Setting default value of 3.")
				Config.logLevel = 3
			}

			Config.identd = section.Key("identd").MustBool(false)
		}

		if section.Name() == "gateway" {
			Config.gateway = section.Key("enabled").MustBool(false)
			Config.gatewayTimeout = section.Key("timeout").MustInt(10)
			Config.gatewayThrottle = section.Key("throttle").MustInt(2)
		}

		if section.Name() == "gateway.webirc" {
			for _, serverAddr := range section.KeyStrings() {
				Config.gatewayWebircPassword[serverAddr] = section.Key(serverAddr).MustString("")
			}
		}

		if strings.Index(section.Name(), "clients") == 0 {
			Config.clientUsername = section.Key("username").MustString("")
			Config.clientRealname = section.Key("realname").MustString("")
		}

		if strings.Index(section.Name(), "fileserving") == 0 {
			if section.Key("enabled").MustBool(false) {
				Config.webroot = section.Key("webroot").MustString("")
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

		if strings.Index(section.Name(), "allowed_origins") == 0 {
			for _, origin := range section.KeyStrings() {
				match, err := glob.Compile(origin)
				if err != nil {
					log.Println("Config section allowed_origins has invalid match, " + origin)
					continue
				}
				Config.remoteOrigins = append(Config.remoteOrigins, match)
			}
		}

		if strings.Index(section.Name(), "gateway.allowed") == 0 {
			for _, origin := range section.KeyStrings() {
				match, err := glob.Compile(origin)
				if err != nil {
					log.Println("Config section allowed_origins has invalid match, " + origin)
					continue
				}
				Config.remoteDestinations = append(Config.remoteDestinations, match)
			}
		}

		if strings.Index(section.Name(), "reverse_proxies") == 0 {
			for _, cidrRange := range section.KeyStrings() {
				_, validRange, cidrErr := net.ParseCIDR(cidrRange)
				if cidrErr != nil {
					log.Println("Config section reverse_proxies has invalid entry, " + cidrRange)
					continue
				}
				Config.reverseProxies = append(Config.reverseProxies, *validRange)
			}
		}
	}

	return nil
}
