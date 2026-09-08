package main

import (
	"context"
	"errors"
	"fmt"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"gocordis-csv-collector/app/host"
	explorerplugin "gocordis-csv-collector/plugins/explorer"
	uiplugin "gocordis-csv-collector/plugins/ui"
)

// App is the Wails transport boundary. It only forwards React RPCs to the
// plugins/ui Host (Application Capability). No business logic lives here:
// no Collector/Storage/Executor access.
type App struct {
	host *host.Host
	ui   *uiplugin.UIComponent
	ad   *uiplugin.Host
	exp  *explorerplugin.ExplorerComponent
	ex   *explorerplugin.Host
	ctx  context.Context
}

// ListSources / ListCollections / GetCollection / ListFiles /
// GetFileMetadata / ListPages / ListPanels / TriggerCollection mirror the
// plugins/ui Host surface.
func (a *App) ListSources() ([]uiplugin.UISource, error) {
	out, ue := a.ad.ListSources()
	return out, ueError(ue)
}

func (a *App) ListCollections() ([]uiplugin.UICollection, error) {
	out, ue := a.ad.ListCollections()
	return out, ueError(ue)
}

func (a *App) GetCollection(req uiplugin.UIGetCollectionRequest) (uiplugin.UICollection, error) {
	out, ue := a.ad.GetCollection(req)
	return out, ueError(ue)
}

func (a *App) ListFiles(req uiplugin.UIListFilesRequest) ([]uiplugin.UIFile, error) {
	out, ue := a.ad.ListFiles(req)
	return out, ueError(ue)
}

func (a *App) GetFileMetadata(req uiplugin.UIFileRequest) (map[string]string, error) {
	out, ue := a.ad.GetFileMetadata(req)
	return out, ueError(ue)
}

func (a *App) ListPages() (uiplugin.UIPageList, error) {
	out, ue := a.ad.ListPages()
	return out, ueError(ue)
}

func (a *App) ListPanels() (uiplugin.UIPanelList, error) {
	out, ue := a.ad.ListPanels()
	return out, ueError(ue)
}

// ListPlugins and ControlPlugin expose the Plugin Explorer Runtime Control
// boundary to Wails. The Explorer component is a plugin; App is only transport.
func (a *App) ListPlugins() (explorerplugin.ExplorerPluginList, error) {
	if a.ex == nil {
		return explorerplugin.ExplorerPluginList{}, errors.New("unavailable: plugin explorer is not active")
	}
	out, ue := a.ex.ListPlugins()
	return out, ueError(ue)
}

func (a *App) ControlPlugin(req explorerplugin.ExplorerControlRequest) (explorerplugin.ExplorerControlResult, error) {
	if a.ex == nil {
		return explorerplugin.ExplorerControlResult{}, errors.New("unavailable: plugin explorer is not active")
	}
	out, ue := a.ex.ControlPlugin(req)
	return out, ueError(ue)
}

// TriggerCollection accepts the command and returns; it never waits for the
// collection run.
func (a *App) TriggerCollection(req uiplugin.UITriggerRequest) error {
	return ueError(a.ad.TriggerCollection(req))
}

func ueError(ue *uiplugin.UIError) error {
	if ue == nil {
		return nil
	}
	// UI Error Contract: only code + message cross to React, never a Go error.
	return errors.New(fmt.Sprintf("%s: %s", ue.Code, ue.Message))
}

// onStartup wires the real Wails Observation Event: UI Plugin Observation ->
// runtime.EventsEmit("observation", UIObservation) -> React EventsOn.
func (a *App) onStartup(ctx context.Context) {
	a.ctx = ctx
	if a.ui == nil {
		return
	}
	a.ui.SetObservationSink(observationSinkFunc(func(ev uiplugin.UIObservation) {
		if a.ctx != nil {
			wailsruntime.EventsEmit(a.ctx, "observation", ev)
		}
	}))
}

func (a *App) onShutdown(_ context.Context) {
	// The Application composition is closed by main after Wails returns; the
	// UI Plugin Effect (observation unsubscribe, registry reset) is unwound by
	// the GOCORDIS Runtime unload, not by a second lifecycle here.
}

type observationSinkFunc func(uiplugin.UIObservation)

func (f observationSinkFunc) NotifyObservation(ev uiplugin.UIObservation) { f(ev) }
