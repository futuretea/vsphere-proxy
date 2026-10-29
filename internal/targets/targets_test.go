package targets

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"sync"
	"testing"

	"github.com/futuretea/vsphere-mcp-server/pkg/core/config"
	"github.com/vmware/govmomi/simulator"
)

func simulatorConfig(t *testing.T, machines int) config.VSphereConfig {
	t.Helper()
	model := simulator.VPX()
	model.Datacenter, model.Cluster, model.ClusterHost = 1, 0, 0
	model.Host, model.Machine = 1, machines
	if err := model.Create(); err != nil {
		t.Fatal("create local vSphere simulator")
	}
	t.Cleanup(model.Remove)
	model.Service.TLS = new(tls.Config)
	server := model.Service.NewServer()
	t.Cleanup(server.Close)
	return config.VSphereConfig{Endpoint: server.URL.String(), Insecure: true}
}

func TestConcurrentRenewUsesOneReplacement(t *testing.T) {
	manager := New(map[string]config.VSphereConfig{"one": {}})
	expired, _ := manager.Service("one")
	results := make(chan any, 8)
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			results <- manager.Renew("one", expired)
		}()
	}
	group.Wait()
	close(results)
	current, _ := manager.Service("one")
	if current == expired {
		t.Fatal("expired service was not replaced")
	}
	for result := range results {
		if result != current {
			t.Fatal("concurrent renew created multiple replacements")
		}
	}
}

func TestTargetQueriesUseSeparateSimulators(t *testing.T) {
	manager := New(map[string]config.VSphereConfig{
		"one": simulatorConfig(t, 1),
		"two": simulatorConfig(t, 2),
	})
	t.Cleanup(func() {
		if err := manager.Close(context.Background()); err != nil {
			t.Error("close simulator sessions")
		}
	})
	for _, tc := range []struct {
		name string
		want int
	}{{"one", 1}, {"two", 2}, {"one", 1}} {
		service, ok := manager.Service(tc.name)
		if !ok {
			t.Fatalf("target %q not found", tc.name)
		}
		result, err := service.ListInventory(context.Background(), "vm", 100)
		if err != nil {
			t.Fatalf("query target %q: %v", tc.name, err)
		}
		var rows []json.RawMessage
		if err := json.Unmarshal([]byte(result), &rows); err != nil {
			t.Fatal("decode simulator inventory")
		}
		if len(rows) != tc.want {
			t.Fatalf("target %q returned %d VMs, want %d", tc.name, len(rows), tc.want)
		}
	}
	if _, ok := manager.Service("missing"); ok {
		t.Fatal("unknown target unexpectedly resolved")
	}
}
