package stateplugin

import (
	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/state"
	"gocordis-csv-collector/plugins/internal/configutil"
)

// Key is the CollectionState capability exposed by this plugin.
var Key = runtime.NewKey[model.CollectionState]("csv.collection.state")

type StateComponent struct {
	typ string
	svc model.CollectionState
}

func (c *StateComponent) Name() string                 { return "state:" + c.typ }
func (c *StateComponent) Inject() []runtime.Dependency { return nil }
func (c *StateComponent) Provide() []runtime.Capability {
	return []runtime.Capability{Key.Capability()}
}
func (c *StateComponent) Apply(ctx *runtime.Context) (runtime.Cleanup, error) {
	if err := runtime.Provide(ctx, Key, c.svc); err != nil {
		return nil, err
	}
	return c.svc.Close, nil
}

// State returns the CollectionState this component provides. The application
// host uses it for the read-only UI projection; collection code keeps going
// through the capability.
func (c *StateComponent) State() model.CollectionState { return c.svc }

// NewState creates the CollectionState Component from configuration.
func NewState(cc config.ComponentConfig) (*StateComponent, error) {
	switch cc.Type {
	case "memory-state":
		return &StateComponent{typ: cc.Type, svc: state.NewMemory()}, nil
	case "file-state":
		path := configutil.OptionalString(cc, "path", "")
		if !state.PathAllowed(path) {
			return nil, errs.Sourcef(errs.ErrInvalidConfig, "state path is invalid")
		}
		fs, err := state.NewFile(path)
		if err != nil {
			return nil, errs.Sourcef(errs.ErrInvalidConfig, "state file: %v", err)
		}
		return &StateComponent{typ: cc.Type, svc: fs}, nil
	default:
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "unknown state type %q", cc.Type)
	}
}
