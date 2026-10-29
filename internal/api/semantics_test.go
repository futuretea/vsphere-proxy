package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/futuretea/vsphere-mcp-server/pkg/core/config"
	"github.com/futuretea/vsphere-proxy/internal/targets"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/alarm"
	"github.com/vmware/govmomi/event"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/methods"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
	"github.com/vmware/govmomi/vim25/xml"
)

type semanticFixture struct {
	client  *govmomi.Client
	manager *targets.Manager
	vm      types.ManagedObjectReference
	perf    chan []byte
}

func newSemanticFixture(t *testing.T) *semanticFixture {
	t.Helper()
	model := simulator.VPX()
	model.Datacenter, model.Cluster, model.ClusterHost, model.Host, model.Machine = 1, 0, 0, 1, 1
	if err := model.Create(); err != nil {
		t.Fatal("create simulator")
	}
	t.Cleanup(model.Remove)
	model.Service.TLS = new(tls.Config)
	upstream := model.Service.NewServer()
	t.Cleanup(upstream.Close)
	client, err := govmomi.NewClient(context.Background(), upstream.URL, true)
	if err != nil {
		t.Fatal("connect simulator")
	}
	t.Cleanup(func() { _ = client.Logout(context.Background()) })
	refs := model.Service.Context.Map.AllReference("VirtualMachine")
	if len(refs) != 1 {
		t.Fatalf("simulator has %d VMs, want 1", len(refs))
	}
	f := &semanticFixture{client: client, vm: refs[0].Reference(), perf: make(chan []byte, 1)}
	frontend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read request", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		recorder := httptest.NewRecorder()
		model.Service.ServeMux.ServeHTTP(recorder, r)
		if bytes.Contains(body, []byte("<QueryPerf ")) || bytes.Contains(body, []byte("<QueryPerf>")) {
			f.perf <- append([]byte(nil), recorder.Body.Bytes()...)
		}
		for key, values := range recorder.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	}))
	t.Cleanup(frontend.Close)
	endpoint := *upstream.URL
	endpoint.Host = strings.TrimPrefix(frontend.URL, "https://")
	f.manager = targets.New(map[string]config.VSphereConfig{"one": {Endpoint: endpoint.String(), Insecure: true}})
	t.Cleanup(func() { _ = f.manager.Close(context.Background()) })
	return f
}

func (f *semanticFixture) query(t *testing.T, path string, result any) {
	t.Helper()
	response := httptest.NewRecorder()
	New(f.manager).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/targets/one/"+path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("%s returned %d, want 200", path, response.Code)
	}
	if err := json.Unmarshal(response.Body.Bytes(), result); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func TestEventAndAlarmQueriesReturnDistinctData(t *testing.T) {
	f := newSemanticFixture(t)
	ctx := context.Background()
	marker := "synthetic.event.marker"
	if err := event.NewManager(f.client.Client).PostEvent(ctx, &types.EventEx{EventTypeId: marker, ObjectType: f.vm.Type, ObjectId: f.vm.Value}); err != nil {
		t.Fatal("post synthetic event")
	}
	var events []struct{ EventTypeId string }
	f.query(t, "events?limit=100", &events)
	found := false
	for _, row := range events {
		found = found || row.EventTypeId == marker
	}
	if !found {
		t.Fatal("events did not include the posted event")
	}
	spec := types.AlarmSpec{Name: "synthetic-alarm", Enabled: true, Expression: &types.OrAlarmExpression{Expression: []types.BaseAlarmExpression{
		&types.EventAlarmExpression{EventTypeId: "synthetic.red", ObjectType: "VirtualMachine", Status: types.ManagedEntityStatusRed},
	}}}
	ref, err := alarm.NewManager(f.client.Client).CreateAlarm(ctx, object.NewRootFolder(f.client.Client), &spec)
	if err != nil {
		t.Fatal("create synthetic alarm definition")
	}
	var definitions []struct {
		Info struct {
			Alarm types.ManagedObjectReference
			Name  string
		}
	}
	f.query(t, "alarms?limit=100", &definitions)
	found = false
	for _, definition := range definitions {
		found = found || (definition.Info.Alarm == *ref && definition.Info.Name == spec.Name)
	}
	if !found {
		t.Fatal("alarm definitions did not include the created alarm")
	}
	var active []json.RawMessage
	f.query(t, "triggered-alarms?limit=100", &active)
	if len(active) != 0 {
		t.Fatalf("untriggered alarm produced %d active states", len(active))
	}
	if err := event.NewManager(f.client.Client).PostEvent(ctx, &types.EventEx{EventTypeId: "synthetic.red", ObjectType: f.vm.Type, ObjectId: f.vm.Value}); err != nil {
		t.Fatal("post alarm event")
	}
	var triggered []struct {
		Name          string
		OverallStatus types.ManagedEntityStatus
		Entity, Alarm types.ManagedObjectReference
	}
	f.query(t, "triggered-alarms?entity_type=VirtualMachine&entity_id="+f.vm.Value, &triggered)
	if len(triggered) != 1 || triggered[0].Name != spec.Name || triggered[0].OverallStatus != types.ManagedEntityStatusRed || triggered[0].Entity != f.vm || triggered[0].Alarm != *ref {
		t.Fatal("triggered alarm did not include the expected identity and status")
	}
}

func TestMetricSamplesPreserveProviderReadings(t *testing.T) {
	f := newSemanticFixture(t)
	var result struct {
		SampleInfo []types.PerfSampleInfo `json:"sample_info"`
		Metrics    []struct {
			ID       int32   `json:"id"`
			Instance string  `json:"instance"`
			Unit     string  `json:"unit"`
			Scale    float64 `json:"scale"`
			Values   []int64 `json:"values"`
		} `json:"metrics"`
	}
	f.query(t, "metric-samples?entity_type=VirtualMachine&entity_id="+f.vm.Value+"&metrics=cpu.usage.average&max_samples=1", &result)
	if len(result.SampleInfo) != 1 || len(result.Metrics) != 1 || len(result.Metrics[0].Values) != 1 {
		t.Fatal("metric response did not contain the requested sample and value")
	}
	if result.Metrics[0].Unit != "percent" || result.Metrics[0].Scale != 0.01 {
		t.Fatal("CPU utilization did not provide the expected percent scale")
	}
	var raw []byte
	select {
	case raw = <-f.perf:
	default:
		t.Fatal("metric query did not reach the simulator")
	}
	var response methods.QueryPerfBody
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.TypeFunc = types.TypeFunc()
	if err := decoder.Decode(&soap.Envelope{Body: &response}); err != nil {
		t.Fatal("decode simulator performance response")
	}
	if response.Res == nil || len(response.Res.Returnval) != 1 {
		t.Fatal("simulator did not return one performance result")
	}
	entity, ok := response.Res.Returnval[0].(*types.PerfEntityMetric)
	if !ok {
		t.Fatal("unexpected simulator performance result")
	}
	for _, series := range entity.Value {
		values, ok := series.(*types.PerfMetricIntSeries)
		if !ok || values.Id.CounterId != result.Metrics[0].ID || values.Id.Instance != result.Metrics[0].Instance {
			continue
		}
		if len(values.Value) != len(entity.SampleInfo) {
			t.Fatal("simulator value and timestamp counts differ")
		}
		for i, sample := range entity.SampleInfo {
			if sample.Timestamp.Equal(result.SampleInfo[0].Timestamp) && values.Value[i] == result.Metrics[0].Values[0] {
				return
			}
		}
	}
	t.Fatal("metric timestamp or value differs from the simulator response")
}
