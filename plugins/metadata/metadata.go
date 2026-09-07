package metadataplugin

import (
	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	"gocordis-csv-collector/app/metadata"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/plugins/internal/configutil"
	sourceplugin "gocordis-csv-collector/plugins/source"
)

// Key is the MetadataExtractor capability exposed by this plugin. The
// Collector requires it through the GOCORDIS Dependency graph (M-14).
var Key = runtime.NewKey[model.MetadataExtractor]("csv.metadata.extractor")

// PathMetadataComponent is an ordinary GOCORDIS Component. It interprets the
// metadata rules configured for one source (A-10: metadata configuration
// follows the Source, never the Collector) and provides the resulting
// MetadataExtractor to the Runtime. It owns no Collector or Runtime lifecycle
// and never touches Fibers, the Orchestrator, the Provider Registry, or the
// Dependency Graph directly (M-15).
type PathMetadataComponent struct {
	sourceID string
	rules    []metadata.Rule
}

func (c *PathMetadataComponent) Name() string { return "path-metadata:" + c.sourceID }

func (c *PathMetadataComponent) Inject() []runtime.Dependency {
	// The source root is read from the active FileSource capability so the
	// root-relative matching semantics never depend on duplicated config.
	return []runtime.Dependency{runtime.Requires(sourceplugin.Key)}
}

func (c *PathMetadataComponent) Provide() []runtime.Capability {
	return []runtime.Capability{Key.Capability()}
}

func (c *PathMetadataComponent) Apply(ctx *runtime.Context) (runtime.Cleanup, error) {
	src, err := runtime.Require(ctx, sourceplugin.Key)
	if err != nil {
		return nil, err
	}
	ex, err := metadata.NewExtractor(src.Root(), c.rules)
	if err != nil {
		return nil, err
	}
	if err := runtime.Provide(ctx, Key, model.MetadataExtractor(ex)); err != nil {
		return nil, err
	}
	return nil, nil
}

// NewMetadata creates the PathMetadata Component from configuration. Rules
// belong to the referenced source; configuration changes are reconciled by
// replacing this Component so the old MetadataExtractor stays valid until the
// new one is active (M-16/M-17).
func NewMetadata(cc config.ComponentConfig) (*PathMetadataComponent, error) {
	sourceID, err := configutil.RequiredString(cc, "source")
	if err != nil {
		return nil, err
	}
	rules, err := metadata.ParseRules(cc.Config["metadata"])
	if err != nil {
		return nil, err
	}
	return &PathMetadataComponent{sourceID: sourceID, rules: rules}, nil
}
