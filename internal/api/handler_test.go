package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/futuretea/vsphere-mcp-server/pkg/core/config"
	"github.com/futuretea/vsphere-proxy/internal/targets"
	"github.com/vmware/govmomi/simulator"
)

func targetConfig(t *testing.T, model *simulator.Model) config.VSphereConfig {
	t.Helper()
	if err := model.Create(); err != nil {
		t.Fatal("create vSphere simulator")
	}
	t.Cleanup(model.Remove)
	model.Service.TLS = new(tls.Config)
	server := model.Service.NewServer()
	t.Cleanup(server.Close)
	return config.VSphereConfig{Endpoint: server.URL.String(), Insecure: true}
}

func TestQueriesThroughHTTP(t *testing.T) {
	first, second := simulator.VPX(), simulator.VPX()
	first.Datacenter, first.Cluster, first.ClusterHost, first.Host, first.Machine = 1, 0, 0, 1, 1
	second.Datacenter, second.Cluster, second.ClusterHost, second.Host, second.Machine = 1, 0, 0, 1, 2
	manager := targets.New(map[string]config.VSphereConfig{
		"one": targetConfig(t, first), "two": targetConfig(t, second), "esxi": targetConfig(t, simulator.ESX()),
	})
	t.Cleanup(func() {
		if err := manager.Close(context.Background()); err != nil {
			t.Error("close target sessions")
		}
	})
	server := httptest.NewServer(New(manager))
	t.Cleanup(server.Close)
	get := func(path string, wantStatus int) []byte {
		t.Helper()
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := resp.Body.Close(); err != nil {
				t.Error("close response body")
			}
		}()
		var value json.RawMessage
		if err := json.NewDecoder(resp.Body).Decode(&value); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if resp.StatusCode != wantStatus {
			t.Fatalf("%s: status %d, want %d; body %s", path, resp.StatusCode, wantStatus, value)
		}
		return value
	}
	var one, two []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(get("/v1/targets/one/inventory?kind=vm", 200), &one); err != nil || len(one) != 1 {
		t.Fatalf("one inventory: %v, count %d", err, len(one))
	}
	if err := json.Unmarshal(get("/v1/targets/two/inventory?kind=vm", 200), &two); err != nil || len(two) != 2 {
		t.Fatalf("two inventory: %v, count %d", err, len(two))
	}
	entity := "entity_type=VirtualMachine&entity_id=" + one[0].ID
	var resource struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(get("/v1/targets/one/resource?"+entity, 200), &resource); err != nil || resource.ID != one[0].ID {
		t.Fatalf("resource did not match requested entity: %v", err)
	}
	var samples struct {
		Entity struct {
			ID string `json:"id"`
		} `json:"entity"`
		Metrics []struct {
			Name string `json:"name"`
		} `json:"metrics"`
	}
	if err := json.Unmarshal(get("/v1/targets/one/metric-samples?"+entity+"&metrics=cpu.usage.average", 200), &samples); err != nil || samples.Entity.ID != one[0].ID || len(samples.Metrics) == 0 || samples.Metrics[0].Name != "cpu.usage.average" {
		t.Fatalf("metric samples did not match requested entity and metric: %v", err)
	}
	checkHTTPResponses(t, get, entity, manager)
}

func checkHTTPResponses(t *testing.T, get func(string, int) []byte, entity string, manager *targets.Manager) {
	t.Helper()
	for _, path := range []string{
		"/v1/targets/one/metrics?" + entity,
		"/v1/targets/one/tasks?limit=1",
		"/v1/targets/one/events?limit=1",
		"/v1/targets/one/alarms?limit=1",
		"/v1/targets/one/triggered-alarms?limit=1",
	} {
		get(path, 200)
	}
	get("/v1/targets/esxi/alarms", 501)
	get("/v1/targets/missing/inventory?kind=vm", 404)
	get("/v1/targets/one/inventory?kind=vm&offset=-1", 400)
	get("/v1/targets/one/metric-samples?"+entity, 400)
	get("/v1/targets/one/events?start_time=2026-02-02T00:00:00Z&end_time=2026-01-01T00:00:00Z", 400)
	get("/v1/targets/one/tasks?entity_type=VirtualMachine", 400)
	for _, path := range []string{"resource", "metric-samples", "triggered-alarms"} {
		get("/v1/targets/one/"+path+"?entity_type=InvalidType&entity_id=vm-1", 400)
	}
	if empty := get("/v1/targets/one/events?start_time=2099-01-01T00:00:00Z", 200); string(empty) != "[]" {
		t.Fatalf("empty events returned %s, want []", empty)
	}
	badRequest := httptest.NewRequest(http.MethodGet, "/v1/targets/one/inventory", nil)
	badRequest.URL.RawQuery = "kind=vm&name=bad%ZZ"
	recorder := httptest.NewRecorder()
	New(manager).ServeHTTP(recorder, badRequest)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("malformed query returned %d, want 400", recorder.Code)
	}
	result := get("/v1/targets/one/resource?entity_type=VirtualMachine&entity_id=not-present", 502)
	if !strings.Contains(string(result), "upstream_error") {
		t.Fatal("failed resource query did not report an upstream error")
	}
}

func TestAlarmQueriesDoNotDependOnEvents(t *testing.T) {
	model := simulator.VPX()
	if err := model.Create(); err != nil {
		t.Fatal("create vSphere simulator")
	}
	t.Cleanup(model.Remove)
	model.Service.TLS = new(tls.Config)
	upstream := model.Service.NewServer()
	t.Cleanup(upstream.Close)
	targetURL := *upstream.URL
	user := targetURL.User.Username()
	password, _ := targetURL.User.Password()
	targetURL.User = nil
	targetURL.Path = ""
	proxy := httputil.NewSingleHostReverseProxy(&targetURL)
	proxy.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} // Local simulator certificate.
	var eventRequests atomic.Int32
	fault := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read request", http.StatusInternalServerError)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if bytes.Contains(body, []byte("QueryEvents")) {
			eventRequests.Add(1)
			http.Error(w, "events unavailable", http.StatusInternalServerError)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(fault.Close)
	manager := targets.New(map[string]config.VSphereConfig{"one": {Endpoint: fault.URL, Username: user, Password: password, Insecure: true}})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	for _, operation := range []string{"alarms", "triggered-alarms"} {
		response := httptest.NewRecorder()
		New(manager).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/targets/one/"+operation, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s returned %d, want 200", operation, response.Code)
		}
	}
	response := httptest.NewRecorder()
	New(manager).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/targets/one/events", nil))
	if response.Code != http.StatusBadGateway || eventRequests.Load() != 1 {
		t.Fatalf("event failure returned %d after %d upstream queries, want 502 and one query", response.Code, eventRequests.Load())
	}
}
