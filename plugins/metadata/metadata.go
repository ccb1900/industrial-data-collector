package metadataplugin

import (
	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	"gocordis-csv-collector/app/metadata"
	"gocordis-csv-collector/app/model"
)

// Key is the single MetadataExtractor capability exposed by this plugin. The
// Collector requires it through the GOCORDIS Dependency graph (M-14/M-MULTI-05).
var Key = runtime.NewKey[model.MetadataExtractor]("csv.metadata.extractor")

// PathMetadataComponent is an ordinary GOCORDIS Component and the ONLY
// MetadataExtractor provider in a Runtime Realm. It holds the metadata rule
// sets of every configured source (rules stay per-source: SourceID -> RuleSet)
// and internally routes Extract by file.SourceID. It never creates one
// provider per source, and it owns no Collector or Runtime lifecycle
// (M-15).
type PathMetadataComponent struct {
	sets []metadata.SourceRuleSet
}

func (c *PathMetadataComponent) Name() string { return "path-metadata" }

func (c *PathMetadataComponent) Inject() []runtime.Dependency { return nil }

func (c *PathMetadataComponent) Provide() []runtime.Capability {
	return []runtime.Capability{Key.Capability()}
}

func (c *PathMetadataComponent) Apply(ctx *runtime.Context) (runtime.Cleanup, error) {
	ex, err := metadata.NewExtractor(c.sets...)
	if err != nil {
		return nil, err
	}
	if err := runtime.Provide(ctx, Key, model.MetadataExtractor(ex)); err != nil {
		return nil, err
	}
	return nil, nil
}

// NewMetadata creates the PathMetadata Component from configuration. The
// component config keeps metadata rules per source (never global): it is an
// array of per-source tables, each with {source, root, metadata}. All entries
// are aggregated into the single MetadataExtractor Provider. A configuration
// change reconciles this one Component, so the old provider stays valid until
// the new one is active (M-16/M-17/M-MULTI-02/M-MULTI-03).
func NewMetadata(cc config.ComponentConfig) (*PathMetadataComponent, error) {
	sets, err := metadata.ParseSourceConfig(cc.Config["sources"])
	if err != nil {
		return nil, err
	}
	return &PathMetadataComponent{sets: sets}, nil
}
