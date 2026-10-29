package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/futuretea/vsphere-mcp-server/pkg/toolset"
	vspheretoolset "github.com/futuretea/vsphere-mcp-server/pkg/toolset/vsphere"
	"github.com/futuretea/vsphere-mcp-server/pkg/vsphere"
	"github.com/futuretea/vsphere-proxy/internal/targets"
	"github.com/vmware/govmomi/fault"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/types"
)

type targetLookup interface {
	Service(string) (*vsphere.Service, bool)
	Renew(string, *vsphere.Service) *vsphere.Service
}

var operations = map[string]string{
	"inventory": "vsphere_list_inventory", "resource": "vsphere_get_resource",
	"metrics": "vsphere_query_metrics", "metric-samples": "vsphere_query_metric_samples",
	"tasks": "vsphere_list_tasks", "events": "vsphere_list_events",
	"alarms": "vsphere_list_alarms", "triggered-alarms": "vsphere_list_triggered_alarms",
}

type Server struct {
	targets targetLookup
}

func New(manager *targets.Manager) http.Handler {
	return &Server{targets: manager}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET is supported")
		return
	}
	name, operation, ok := route(r.URL.Path)
	toolName, known := operations[operation]
	if !ok || !known {
		writeError(w, http.StatusNotFound, "not_found", "Unknown query path")
		return
	}
	service, ok := s.targets.Service(name)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "Unknown target")
		return
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid query string")
		return
	}
	params, err := queryParams(operation, values)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := runQuery(r.Context(), service, toolName, operation, params)
	if fault.Is(err, &types.NotAuthenticated{}) {
		service = s.targets.Renew(name, service)
		result, err = runQuery(r.Context(), service, toolName, operation, params)
	}
	if err != nil {
		if unsupported(err) {
			writeError(w, http.StatusNotImplemented, "unsupported", "Query is not supported by the target")
			return
		}
		writeError(w, http.StatusBadGateway, "upstream_error", "Failed to query target")
		return
	}
	if !json.Valid([]byte(result)) {
		writeError(w, http.StatusInternalServerError, "internal_error", "Invalid query result")
		return
	}
	if strings.TrimSpace(result) == "null" && operation != "resource" && operation != "metric-samples" {
		result = "[]"
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(result))
}

func unsupported(err error) bool {
	// These errors are returned by the pinned vSphere service when managers are absent.
	return errors.Is(err, object.ErrNotSupported) || fault.Is(err, &types.NotSupported{}) || fault.Is(err, &types.NotImplemented{}) || err.Error() == "performance metrics are not available on this target" || err.Error() == "tasks are not available on this target"
}

func runQuery(ctx context.Context, service *vsphere.Service, toolName, operation string, params map[string]any) (string, error) {
	handler, err := queryHandler(ctx, service, toolName, operation)
	if err != nil {
		return "", err
	}
	return handler(ctx, params)
}

func queryHandler(ctx context.Context, service *vsphere.Service, toolName, operation string) (toolset.ToolHandler, error) {
	events := false
	if operation == "events" {
		capabilities, err := service.Capabilities(ctx)
		if err != nil {
			return nil, err
		}
		events = capabilities.Events
	}
	for _, tool := range vspheretoolset.NewToolset(service, events, true).GetTools() {
		if tool.Tool.Name == toolName {
			return tool.Handler, nil
		}
	}
	return nil, object.ErrNotSupported
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}
