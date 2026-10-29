package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadTargetsFromEnvironment(t *testing.T) {
	t.Setenv("TEST_PROXY_USERNAME", "example-user")
	t.Setenv("TEST_PROXY_PASSWORD", "example-password")
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"listen":"127.0.0.1:8080","targets":{"one":{"endpoint":"https://one.example.invalid/sdk","username_env":"TEST_PROXY_USERNAME","password_env":"TEST_PROXY_PASSWORD"},"two":{"endpoint":"https://two.example.invalid/sdk","username_env":"TEST_PROXY_USERNAME","password_env":"TEST_PROXY_PASSWORD","insecure":true}}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Targets) != 2 || got.Targets["one"].Username != "example-user" || got.Targets["two"].Password != "example-password" || got.Targets["one"].Insecure || !got.Targets["two"].Insecure {
		t.Fatal("target credentials or TLS verification not configured as expected")
	}
}

func TestConfigurationRejectsEndpointQueryAndFragment(t *testing.T) {
	t.Setenv("TEST_PROXY_USERNAME", "example-user")
	t.Setenv("TEST_PROXY_PASSWORD", "example-password")
	for _, suffix := range []string{"?", "?password=SYNTHETIC_PLACEHOLDER", "#", "#token=SYNTHETIC_PLACEHOLDER"} {
		t.Run(suffix, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			data, err := json.Marshal(map[string]any{"listen": "127.0.0.1:8080", "targets": map[string]any{
				"one": map[string]any{"endpoint": "https://host.example.invalid/sdk" + suffix, "username_env": "TEST_PROXY_USERNAME", "password_env": "TEST_PROXY_PASSWORD"},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			_, err = Load(path)
			if err == nil || strings.Contains(err.Error(), "SYNTHETIC_PLACEHOLDER") {
				t.Fatal("endpoint with query or fragment was accepted or exposed")
			}
		})
	}
}

func TestConfigurationRejectsCredentialsInEndpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"listen":"127.0.0.1:8080","targets":{"one":{"endpoint":"https://example-user:example-password@one.example.invalid/sdk"}}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || strings.Contains(err.Error(), "example-password") {
		t.Fatal("endpoint with embedded credentials was accepted or exposed")
	}
}
