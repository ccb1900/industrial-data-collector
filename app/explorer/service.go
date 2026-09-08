package explorer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	appconfig "gocordis-csv-collector/app/config"
)

const waitTimeout = 10 * time.Second

// OwnedProvider returns the Config Controller's current applied snapshot. The
// service reads it for every inspection; it never caches lifecycle truth.
type OwnedProvider func() []config.OwnedComponent

// Service implements the Application Plugin Explorer inspection/control
// boundary. Desired components come from the configuration already handed to
// the Config Controller. Runtime state comes from each Controller-owned Fiber.
type Service struct {
	mu        sync.Mutex
	owned     OwnedProvider
	desired   []config.ComponentConfig
	protected map[string]string
}

// New returns an empty explorer Service. SetOwned must be called before the
// Service is used; SetDesired is refreshed after every successful Reconcile.
func New() *Service {
	return &Service{protected: map[string]string{}}
}

func (s *Service) SetOwned(owned OwnedProvider) {
	s.mu.Lock()
	s.owned = owned
	s.mu.Unlock()
}

// SetDesired installs the last successful desired component set. It also marks
// Console-critical components so the Explorer cannot remove the very host it
// renders inside.
func (s *Service) SetDesired(cfg config.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.desired = make([]config.ComponentConfig, len(cfg.Components))
	copy(s.desired, cfg.Components)
	s.protected = map[string]string{}
	for _, cc := range cfg.Components {
		switch cc.Type {
		case "plugin-explorer":
			s.protected[cc.ID] = "plugin-explorer hosts its own Console UI"
		case "ui":
			s.protected[cc.ID] = "ui host must stay active while the Console is open"
		case "query-provider":
			s.protected[cc.ID] = "query provider is required by the UI Host"
		}
	}
}

// Plugins returns a deterministic renderable snapshot ordered by the desired
// component order. State is read from Runtime fibers; no UI-owned enabled map
// is maintained here.
func (s *Service) Plugins() []Plugin {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Plugin, 0, len(s.desired))
	owned := map[string]config.OwnedComponent{}
	if s.owned != nil {
		for _, o := range s.owned() {
			owned[o.ID] = o
		}
	}
	for _, cc := range s.desired {
		p := Plugin{
			ID:           cc.ID,
			Name:         appconfig.DisplayName(cc.Type),
			Type:         cc.Type,
			State:        "Gone",
			Components:   []string{},
			Capabilities: []string{},
			Controllable: s.protected[cc.ID] == "",
		}
		if o, ok := owned[cc.ID]; ok && o.Fiber != nil {
			p.State = o.Fiber.State().String()
			p.Components = []string{o.Fiber.Name()}
			caps := make([]string, 0, len(o.Fiber.Component().Provide()))
			for _, c := range o.Fiber.Component().Provide() {
				caps = append(caps, capabilityLabel(c.String()))
			}
			sort.Strings(caps)
			p.Capabilities = caps
		}
		out = append(out, p)
	}
	return out
}

func capabilityLabel(s string) string {
	if i := strings.Index(s, `("`); i >= 0 {
		rest := s[i+2:]
		if j := strings.LastIndex(rest, `")`); j >= 0 {
			return rest[:j]
		}
	}
	return s
}

// Control enables or disables one discovered component through Runtime
// Fiber.Load/Dispose. It waits for the Runtime-visible terminal state so the
// returned result never lets a UI assume Active after a failed Load.
func (s *Service) Control(ctx context.Context, id string, enable bool) ControlResult {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if reason := s.protected[id]; reason != "" {
		state, err := s.currentState(id)
		if err != nil {
			return ControlResult{PluginID: id, Rejected: true, State: state, Error: fmt.Sprintf("%s: %s", reason, errText(err))}
		}
		return ControlResult{PluginID: id, Rejected: true, State: state, Error: reason}
	}
	var cc config.ComponentConfig
	found := false
	for _, c := range s.desired {
		if c.ID == id {
			cc = c
			found = true
			break
		}
	}
	if !found {
		return ControlResult{PluginID: id, Rejected: true, State: "Gone", Error: "plugin is not part of the current runtime composition"}
	}

	var fiber *runtime.Fiber
	if s.owned != nil {
		for _, o := range s.owned() {
			if o.ID == id && o.Fiber != nil {
				fiber = o.Fiber
				break
			}
		}
	}
	if fiber == nil {
		return ControlResult{PluginID: id, Rejected: true, State: "Gone", Error: fmt.Sprintf("component %q is declared but not owned by the runtime", cc.ID)}
	}

	wait, cancel := context.WithTimeout(ctx, waitTimeout)
	defer cancel()

	if enable {
		if fiber.State() == runtime.StateFailed {
			if err := fiber.Dispose(); err != nil {
				return resultFor(id, fiber, true, fmt.Errorf("retry activate %q: %w", id, err))
			}
			if err := fiber.Gone(wait); err != nil {
				return resultFor(id, fiber, true, fmt.Errorf("retry activate %q: %w", id, err))
			}
		}
		if err := fiber.Load(); err != nil {
			return resultFor(id, fiber, true, fmt.Errorf("activate %q: %w", id, err))
		}
		if err := fiber.Ready(wait); err != nil {
			return resultFor(id, fiber, true, fmt.Errorf("activate %q: %w", id, err))
		}
		return ControlResult{PluginID: id, Accepted: true, State: fiber.State().String()}
	}

	if err := fiber.Dispose(); err != nil {
		return resultFor(id, fiber, true, fmt.Errorf("deactivate %q: %w", id, err))
	}
	if err := fiber.Gone(wait); err != nil {
		return resultFor(id, fiber, true, fmt.Errorf("deactivate %q: %w", id, err))
	}
	return ControlResult{PluginID: id, Accepted: true, State: fiber.State().String()}
}

// currentState is used for rejection responses. It reads Runtime only.
func (s *Service) currentState(id string) (string, error) {
	if s.owned == nil {
		return "Gone", errors.New("runtime inspection unavailable")
	}
	for _, o := range s.owned() {
		if o.ID == id && o.Fiber != nil {
			return o.Fiber.State().String(), o.Fiber.Err()
		}
	}
	return "Gone", nil
}

func resultFor(id string, f *runtime.Fiber, failed bool, err error) ControlResult {
	return ControlResult{PluginID: id, Failed: failed, State: f.State().String(), Error: errText(err)}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
