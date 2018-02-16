package webircgateway

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	GatewayName    string
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
	GatewayName           string
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
	ClientHostname        string
	Identd                bool
	RequiresVerification  bool
	ReCaptchaSecret       string
	ReCaptchaKey          string
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
	Config.ReCaptchaSecret = ""
	Config.ReCaptchaKey = ""
	Config.RequiresVerification = false
	Config.ClientRealname = ""
	Config.ClientUsername = ""
	Config.ClientHostname = ""

	for _, section := range cfg.Sections() {
		if strings.Index(section.Name(), "DEFAULT") == 0 {
			Config.LogLevel = section.Key("logLevel").MustInt(3)
			if Config.LogLevel < 1 || Config.LogLevel > 3 {
				logOut(3, "Config option logLevel must be between 1-3. Setting default value of 3.")
				Config.LogLevel = 3
			}

			Config.Identd = section.Key("identd").MustBool(false)

			Config.GatewayName = section.Key("gateway_name").MustString("")
			if strings.Contains(Config.GatewayName, " ") {
				logOut(3, "Config option gateway_name must not contain spaces")
				Config.GatewayName = ""
			}
		}

		if section.Name() == "verify" {
			captchaSecret := section.Key("recaptcha_secret").MustString("")
			captchaKey := section.Key("recaptcha_key").MustString("")
			if captchaSecret != "" && captchaKey != "" {
				Config.RequiresVerification = true
				Config.ReCaptchaSecret = captchaSecret
			}
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
			Config.ClientHostname = section.Key("hostname").MustString("")
		}

		if strings.Index(section.Name(), "fileserving") == 0 {
			if section.Key("enabled").MustBool(false) {
				Config.Webroot = section.Key("webroot").MustString("")
			}
		}

		if strings.Index(section.Name(), "server.") == 0 {
			server := ConfigServer{}
			server.LocalAddr = confKeyAsString(section.Key("bind"), "127.0.0.1")
			server.Port = confKeyAsInt(section.Key("port"), 80)
			server.TLS = confKeyAsBool(section.Key("tls"), false)
			server.CertFile = confKeyAsString(section.Key("cert"), "")
			server.KeyFile = confKeyAsString(section.Key("key"), "")
			server.LetsEncryptCacheFile = confKeyAsString(section.Key("letsencrypt_cache"), "")

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

			upstream.GatewayName = section.Key("gateway_name").MustString("")
			if strings.Contains(upstream.GatewayName, " ") {
				logOut(3, "Config option gateway_name must not contain spaces")
				upstream.GatewayName = ""
			}

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
					logOut(3, "Config section allowed_origins has invalid match, "+origin)
					continue
				}
				Config.RemoteOrigins = append(Config.RemoteOrigins, match)
			}
		}

		if strings.Index(section.Name(), "gateway.whitelist") == 0 {
			for _, origin := range section.KeyStrings() {
				match, err := glob.Compile(origin)
				if err != nil {
					logOut(3, "Config section gateway.whitelist has invalid match, "+origin)
					continue
				}
				Config.GatewayWhitelist = append(Config.GatewayWhitelist, match)
			}
		}

		if strings.Index(section.Name(), "reverse_proxies") == 0 {
			for _, cidrRange := range section.KeyStrings() {
				_, validRange, cidrErr := net.ParseCIDR(cidrRange)
				if cidrErr != nil {
					logOut(3, "Config section reverse_proxies has invalid entry, "+cidrRange)
					continue
				}
				Config.ReverseProxies = append(Config.ReverseProxies, *validRange)
			}
		}
	}

	return nil
}

func confKeyAsString(key *ini.Key, def string) string {
	val := def

	str := key.String()
	if len(str) > 1 && str[:1] == "$" {
		val = os.Getenv(str[1:])
	} else {
		val = key.MustString(def)
	}

	return val
}

func confKeyAsInt(key *ini.Key, def int) int {
	val := def

	str := key.String()
	if len(str) > 1 && str[:1] == "$" {
		envVal := os.Getenv(str[1:])
		envValInt, err := strconv.Atoi(envVal)
		if err == nil {
			val = envValInt
		}
	} else {
		val = key.MustInt(def)
	}

	return val
}

func confKeyAsBool(key *ini.Key, def bool) bool {
	val := def

	str := key.String()
	if len(str) > 1 && str[:1] == "$" {
		envVal := os.Getenv(str[1:])
		if envVal == "0" || envVal == "false" || envVal == "no" {
			val = false
		} else {
			val = true
		}
	} else {
		val = key.MustBool(def)
	}

	return val
}
