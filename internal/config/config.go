package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	vsphereconfig "github.com/futuretea/vsphere-mcp-server/pkg/core/config"
)

type target struct {
	Endpoint    string `json:"endpoint"`
	UsernameEnv string `json:"username_env"`
	PasswordEnv string `json:"password_env"`
	Insecure    bool   `json:"insecure"`
}

type file struct {
	Listen  string            `json:"listen"`
	Targets map[string]target `json:"targets"`
}

type Config struct {
	Listen  string
	Targets map[string]vsphereconfig.VSphereConfig
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read configuration: %w", err)
	}
	var input file
	if err := json.Unmarshal(data, &input); err != nil {
		return Config{}, fmt.Errorf("parse configuration")
	}
	if input.Listen == "" || len(input.Targets) == 0 {
		return Config{}, fmt.Errorf("listen and at least one target are required")
	}
	result := Config{Listen: input.Listen, Targets: make(map[string]vsphereconfig.VSphereConfig, len(input.Targets))}
	for name, entry := range input.Targets {
		endpoint, err := url.Parse(entry.Endpoint)
		if name == "" || strings.Contains(name, "/") || strings.ContainsAny(entry.Endpoint, "?#") || err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil {
			return Config{}, fmt.Errorf("invalid target name or HTTPS endpoint")
		}
		username, userOK := os.LookupEnv(entry.UsernameEnv)
		password, passwordOK := os.LookupEnv(entry.PasswordEnv)
		if !userOK || !passwordOK || username == "" || password == "" {
			return Config{}, fmt.Errorf("target credentials are not configured")
		}
		result.Targets[name] = vsphereconfig.VSphereConfig{Endpoint: entry.Endpoint, Username: username, Password: password, Insecure: entry.Insecure}
	}
	return result, nil
}
