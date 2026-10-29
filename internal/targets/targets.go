package targets

import (
	"context"
	"errors"
	"sync"

	"github.com/futuretea/vsphere-mcp-server/pkg/core/config"
	"github.com/futuretea/vsphere-mcp-server/pkg/vsphere"
)

// Manager owns a separate vSphere service for each configured target.
type Manager struct {
	services map[string]*target
}

type target struct {
	mu      sync.Mutex
	config  config.VSphereConfig
	service *vsphere.Service
}

func New(configs map[string]config.VSphereConfig) *Manager {
	services := make(map[string]*target, len(configs))
	for name, cfg := range configs {
		services[name] = &target{config: cfg, service: vsphere.NewService(cfg)}
	}
	return &Manager{services: services}
}

func (m *Manager) Service(name string) (*vsphere.Service, bool) {
	target, ok := m.services[name]
	if !ok {
		return nil, false
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	return target.service, true
}

// Renew replaces an expired session only if another request has not already done so.
func (m *Manager) Renew(name string, expired *vsphere.Service) *vsphere.Service {
	target := m.services[name]
	target.mu.Lock()
	defer target.mu.Unlock()
	if target.service == expired {
		target.service = vsphere.NewService(target.config)
	}
	return target.service
}

func (m *Manager) Close(ctx context.Context) error {
	var result error
	for _, target := range m.services {
		result = errors.Join(result, target.service.Close(ctx))
	}
	return result
}
