package plugin

import (
	"fmt"
	"log/slog"
)

// Manager holds registered plugins and manages their lifecycle.
type Manager struct {
	all           []Plugin
	transport     []TransportPlugin
	llm           []LLMPlugin
	observability []ObservabilityPlugin
	logger        *slog.Logger
}

// NewManager creates a new plugin manager.
func NewManager(logger *slog.Logger) *Manager {
	return &Manager{logger: logger}
}

// Register adds a plugin to the manager. A plugin implementing multiple
// interfaces (e.g. both TransportPlugin and LLMPlugin) is added to all
// matching slices.
func (m *Manager) Register(p Plugin) {
	m.all = append(m.all, p)
	if tp, ok := p.(TransportPlugin); ok {
		m.transport = append(m.transport, tp)
	}
	if lp, ok := p.(LLMPlugin); ok {
		m.llm = append(m.llm, lp)
	}
	if op, ok := p.(ObservabilityPlugin); ok {
		m.observability = append(m.observability, op)
	}
	m.logger.Info("plugin registered", "name", p.Name())
}

// InitAll initializes all registered plugins. configs is keyed by plugin name.
func (m *Manager) InitAll(configs map[string]map[string]any) error {
	for _, p := range m.all {
		cfg := configs[p.Name()]
		if err := p.Init(cfg); err != nil {
			return fmt.Errorf("plugin %q init failed: %w", p.Name(), err)
		}
		m.logger.Info("plugin initialized", "name", p.Name())
	}
	return nil
}

// CloseAll shuts down all plugins in reverse registration order.
// Errors are logged but not returned (fail-open).
func (m *Manager) CloseAll() {
	for i := len(m.all) - 1; i >= 0; i-- {
		p := m.all[i]
		if err := p.Close(); err != nil {
			m.logger.Error("plugin close error", "name", p.Name(), "error", err)
		}
	}
}

// TransportPlugins returns registered transport plugins in order.
func (m *Manager) TransportPlugins() []TransportPlugin { return m.transport }

// LLMPlugins returns registered LLM plugins in order.
func (m *Manager) LLMPlugins() []LLMPlugin { return m.llm }

// ObservabilityPlugins returns registered observability plugins in order.
func (m *Manager) ObservabilityPlugins() []ObservabilityPlugin { return m.observability }
