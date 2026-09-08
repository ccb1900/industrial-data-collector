// Package explorer provides the Application-level inspection/control view
// used by the Plugin Explorer. It is not a second lifecycle: desired
// components come from the existing Config Controller and every transition is
// expressed through Runtime Fiber.Load/Dispose.
package explorer

// Plugin is one renderable runtime plugin row. State always mirrors the
// Runtime FiberState (including Gone for a disposed component); it is never a
// UI-owned enabled flag.
type Plugin struct {
	ID           string
	Name         string
	Type         string
	State        string
	Components   []string
	Capabilities []string
	Controllable bool
}

// ControlResult is the runtime control outcome returned to a Console UI.
// Accepted/Rejected/Failed are mutually descriptive, not inferred by the
// frontend.
type ControlResult struct {
	PluginID string
	Accepted bool
	Rejected bool
	Failed   bool
	State    string
	Error    string
}
