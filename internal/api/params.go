package api

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func route(path string) (string, string, bool) {
	parts := strings.Split(path, "/")
	if len(parts) != 5 || parts[0] != "" || parts[1] != "v1" || parts[2] != "targets" || parts[3] == "" || parts[4] == "" {
		return "", "", false
	}
	return parts[3], parts[4], true
}

func queryParams(operation string, values url.Values) (map[string]any, error) {
	params := make(map[string]any)
	for _, name := range []string{"kind", "datacenter_id", "name", "entity_type", "entity_id", "instance", "start_time", "end_time"} {
		if _, ok := values[name]; ok {
			params[name] = strings.TrimSpace(values.Get(name))
		}
	}
	if err := parseNumbers(values, params); err != nil {
		return nil, err
	}
	if err := validateQuery(operation, values, params); err != nil {
		return nil, err
	}
	return params, validateTimes(params)
}

func parseNumbers(values url.Values, params map[string]any) error {
	for _, spec := range []struct {
		name string
		min  int
		max  int
	}{{"limit", 1, 1000}, {"offset", 0, 2147483647}, {"max_samples", 1, 1000}, {"interval_seconds", 1, 2147483647}} {
		if _, ok := values[spec.name]; !ok {
			continue
		}
		n, err := strconv.Atoi(values.Get(spec.name))
		if err != nil || n < spec.min || n > spec.max {
			return fmt.Errorf("%s must be between %d and %d", spec.name, spec.min, spec.max)
		}
		params[spec.name] = float64(n)
	}
	return nil
}

func validateQuery(operation string, values url.Values, params map[string]any) error {
	if operation == "inventory" && params["kind"] == nil {
		return fmt.Errorf("kind is required")
	}
	if operation == "inventory" {
		kinds := map[string]bool{"vm": true, "host": true, "datastore": true, "network": true, "datacenter": true, "cluster": true, "resource_pool": true, "folder": true, "distributed_portgroup": true}
		if !kinds[params["kind"].(string)] {
			return fmt.Errorf("kind must identify a supported inventory type")
		}
	}
	entityType, _ := params["entity_type"].(string)
	entityID, _ := params["entity_id"].(string)
	required := operation == "resource" || operation == "metrics" || operation == "metric-samples"
	if (required || entityType != "" || entityID != "") && (entityType == "" || entityID == "") {
		return fmt.Errorf("entity_type and entity_id must be provided together")
	}
	if entityType != "" && (operation == "resource" || operation == "metric-samples" || operation == "triggered-alarms") && !supportedResourceType(entityType) {
		return fmt.Errorf("entity_type must identify a supported resource type")
	}
	if operation == "metric-samples" {
		metrics := values["metrics"]
		if len(metrics) == 0 || len(metrics) > 1000 {
			return fmt.Errorf("metrics must contain between 1 and 1000 names")
		}
		for _, metric := range metrics {
			if strings.TrimSpace(metric) == "" {
				return fmt.Errorf("metrics must not contain empty names")
			}
		}
		params["metrics"] = metrics
	}
	return nil
}

func supportedResourceType(entityType string) bool {
	switch entityType {
	case "VirtualMachine", "HostSystem", "Datastore", "Network", "OpaqueNetwork", "DistributedVirtualPortgroup", "DistributedVirtualSwitch", "VmwareDistributedVirtualSwitch", "Datacenter", "ClusterComputeResource", "ResourcePool", "Folder", "StoragePod":
		return true
	default:
		return false
	}
}

func validateTimes(params map[string]any) error {
	var bounds [2]time.Time
	for i, name := range []string{"start_time", "end_time"} {
		text, _ := params[name].(string)
		if text == "" {
			continue
		}
		value, err := time.Parse(time.RFC3339, text)
		if err != nil {
			return fmt.Errorf("%s must be an RFC3339 timestamp", name)
		}
		bounds[i] = value
	}
	if !bounds[0].IsZero() && !bounds[1].IsZero() && bounds[0].After(bounds[1]) {
		return fmt.Errorf("start_time must not be after end_time")
	}
	return nil
}
