package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	proxyconfig "github.com/futuretea/vsphere-proxy/internal/config"
	"github.com/futuretea/vsphere-proxy/internal/targets"
	"github.com/vmware/govmomi/simulator"
)

func TestConfiguredTLSVerification(t *testing.T) {
	target := targetConfig(t, simulator.VPX())
	endpoint, err := url.Parse(target.Endpoint)
	if err != nil || endpoint.User == nil {
		t.Fatal("simulator endpoint is missing credentials")
	}
	username := endpoint.User.Username()
	password, ok := endpoint.User.Password()
	if !ok {
		t.Fatal("simulator endpoint is missing a password")
	}
	endpoint.User = nil
	t.Setenv("TEST_TLS_USER", username)
	t.Setenv("TEST_TLS_PASSWORD", password)

	config := map[string]any{"listen": "127.0.0.1:8080", "targets": map[string]any{
		"verified": map[string]any{"endpoint": endpoint.String(), "username_env": "TEST_TLS_USER", "password_env": "TEST_TLS_PASSWORD"},
		"insecure": map[string]any{"endpoint": endpoint.String(), "username_env": "TEST_TLS_USER", "password_env": "TEST_TLS_PASSWORD", "insecure": true},
	}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := proxyconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	manager := targets.New(cfg.Targets)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	for _, tc := range []struct {
		name string
		want int
	}{{"verified", http.StatusBadGateway}, {"insecure", http.StatusOK}} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/v1/targets/"+tc.name+"/inventory?kind=vm", nil)
		New(manager).ServeHTTP(recorder, request)
		if recorder.Code != tc.want {
			t.Fatalf("target %s returned %d, want %d", tc.name, recorder.Code, tc.want)
		}
	}
}
