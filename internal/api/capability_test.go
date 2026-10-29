package api

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/futuretea/vsphere-mcp-server/pkg/core/config"
	"github.com/futuretea/vsphere-proxy/internal/targets"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/simulator/sim25"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/types"
)

func TestMissingManagersReturnUnsupported(t *testing.T) {
	model := simulator.VPX()
	if err := model.Create(); err != nil {
		t.Fatal("create simulator")
	}
	t.Cleanup(model.Remove)
	si := model.Service.Context.Map.Get(vim25.ServiceInstance).(*simulator.ServiceInstance)
	si.Content.PerfManager = nil
	si.Content.TaskManager = nil
	model.Service.TLS = new(tls.Config)
	server := model.Service.NewServer()
	t.Cleanup(server.Close)
	manager := targets.New(map[string]config.VSphereConfig{"one": {Endpoint: server.URL.String(), Insecure: true}})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	for _, path := range []string{
		"metrics?entity_type=VirtualMachine&entity_id=vm-1",
		"metric-samples?entity_type=VirtualMachine&entity_id=vm-1&metrics=cpu.usage.average",
		"tasks",
	} {
		response := httptest.NewRecorder()
		New(manager).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/targets/one/"+path, nil))
		if response.Code != http.StatusNotImplemented {
			t.Fatalf("%s returned %d, want 501", path, response.Code)
		}
	}
}

func TestUnsupportedProviderFaultsReturn501(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fault types.BaseMethodFault
	}{{"not_supported", &types.NotSupported{}}, {"not_implemented", &types.NotImplemented{}}} {
		t.Run(tc.name, func(t *testing.T) {
			model := simulator.VPX()
			cfg := targetConfig(t, model)
			model.Service.AddFaultRule(&simulator.FaultInjectionRule{
				MethodName: "QueryAvailablePerfMetric", ObjectType: "*", ObjectName: "*",
				Probability: 1, Enabled: true, FaultType: simulator.FaultTypeCustom, Fault: tc.fault,
			})
			manager := targets.New(map[string]config.VSphereConfig{"one": cfg})
			t.Cleanup(func() { _ = manager.Close(context.Background()) })
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/v1/targets/one/metrics?entity_type=VirtualMachine&entity_id=vm-1", nil)
			New(manager).ServeHTTP(response, request)
			if response.Code != http.StatusNotImplemented {
				t.Fatalf("unsupported provider fault returned %d, want 501", response.Code)
			}
		})
	}
}

func TestExpiredSessionRecovers(t *testing.T) {
	model := simulator.VPX()
	if err := model.Create(); err != nil {
		t.Fatal("create simulator")
	}
	t.Cleanup(model.Remove)
	model.Service.TLS = new(tls.Config)
	server := model.Service.NewServer()
	t.Cleanup(server.Close)
	client, err := govmomi.NewClient(context.Background(), server.URL, true)
	if err != nil {
		t.Fatal("connect simulator")
	}
	if err := sim25.SetSessionTimeout(context.Background(), client.Client, time.Second); err != nil {
		t.Fatal("set simulator session timeout")
	}
	manager := targets.New(map[string]config.VSphereConfig{"one": {Endpoint: server.URL.String(), Insecure: true}})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	query := func() int {
		response := httptest.NewRecorder()
		New(manager).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/targets/one/inventory?kind=vm", nil))
		return response.Code
	}
	if got := query(); got != http.StatusOK {
		t.Fatalf("initial query returned %d", got)
	}
	previous, _ := manager.Service("one")
	// The simulator checks idleness periodically; wait beyond two checks.
	time.Sleep(2500 * time.Millisecond)
	if got := query(); got != http.StatusOK {
		t.Fatalf("query after session expiration returned %d", got)
	}
	current, _ := manager.Service("one")
	if current == previous {
		t.Fatal("expired session did not trigger replacement")
	}
}
