# File Business Metadata / Path Metadata — Implementation v0.1

This document records how the File Business Metadata / Path Metadata
Specification v0.1 is implemented in this repository. The specification adds a
business interpretation layer over discovered CSV files without touching
`FileIdentity`, `gocordis/runtime`, or `gocordis/extensions`.

## 1. Core boundary

```text
FileIdentity            technical identity: "which input file is this?"
Metadata                business interpretation: "what does this file represent?"
FileIdentity + Metadata = FileDescriptor
```

`FileIdentity.Identity()` is built from `SourceID + Path + Hash` (or
`SourceID + Path + Size + ModTime`) and never includes `Metadata`. Changing
metadata rules therefore never makes an already collected file look new
(M-11/M-18).

## 2. Where the code lives

| Area | Location | Responsibility |
| --- | --- | --- |
| Contracts and value objects | `app/model/metadata.go` | `Metadata`, `FileDescriptor`, `MetadataExtractor`, `FileSource.Root()` |
| Pure extraction engine | `app/metadata/` | per-source rule sets (`SourceRuleSet`), template grammar, matcher, `Extractor` |
| GOCORDIS plugin | `plugins/metadata/` | `PathMetadataComponent` providing the `MetadataExtractor` capability |
| Collector wiring | `plugins/collector/` | requires the metadata capability through the Dependency graph |
| Application validation | `app/config/validate.go` | rejects invalid metadata config before Runtime mutation |

The GOCORDIS Runtime is unchanged; all types and plugin code belong to this
application (`gocordis-csv-collector`).

## 3. Configuration

Metadata rules are configured per source and never on the Collector. One
`path-metadata` component is the single `MetadataExtractor` Provider of the
Realm; its config aggregates per-source rule sets (`SourceID -> RuleSet`):

```toml
[[components]]
id = "production-metadata"
type = "path-metadata"

[components.config]

[[components.config.sources]]
source = "source-a"
root = "./data/a"

[[components.config.sources.metadata]]
name = "line"
from = "path"
pattern = "{line}/{station}/{date}/*.csv"
required = true

[[components.config.sources]]
source = "source-b"
root = "./data/b"

[[components.config.sources.metadata]]
name = "product"
from = "path"
pattern = "{product}/{batch}/{date}.csv"
required = true
```

`path-metadata` is an ordinary GOCORDIS Component (A-04, spec section 33). It
provides exactly one `MetadataExtractor` capability; it never creates one
provider per source. `root` is kept per source entry and application validation
enforces it equals the referenced source component's root. The Collector
declares `runtime.Requires(metadataplugin.Key)` and receives the single
`MetadataExtractor` through the Dependency graph (M-14/M-MULTI-05); it never
routes by source id itself.

A configuration with a Collector but no `path-metadata` component is rejected
during application validation, as is more than one `path-metadata` component
per Runtime realm (exactly one provider per Realm). Multiple sources inside the
one component are allowed with completely different rule sets.

## 3.1 Multi-source routing

`Extractor` holds one compiled rule set per `SourceID`. `Extract(file)` selects
the rule set for `file.SourceID`:

```text
one Runtime Realm
  └── MetadataExtractor Provider
        ├── Source A -> RuleSet A
        ├── Source B -> RuleSet B
        └── Source C -> RuleSet C
```

A file whose `SourceID` has no registered rule set gets empty metadata (v0.1
semantics for "source without metadata rules"), never an error. Reloading one
source's rules rebuilds the provider with that source's new rule set; other
sources keep their rule sets unchanged (M-MULTI-02). Invalid new rules for one
source are rejected before Runtime mutation, so the previous valid provider
(including other sources' rule sets) stays effective (M-MULTI-03).

## 4. Rule semantics

- One rule produces exactly one metadata key: its `name`.
- A rule's own `name` must appear as a `{name}` capture in its pattern.
- Additional `{field}` captures in the same pattern only describe the business
  layout and are not stored.
- Key grammar: `[A-Za-z_][A-Za-z0-9_]*`.
- Duplicate keys within one source's rule set are rejected (M-09). The same
  key may appear in different sources because each source is its own
  namespace.
- `required` defaults to `true`; a missing required rule is a file-level
  `ErrMetadataExtraction` error, a missing optional rule leaves the key absent
  (M-07/M-08). Empty capture values are treated as "no meaningful value".
- Template grammar per segment: literal text, `{field}` capture, and `*`
  wildcard. Segments are separated by `/`; the number of pattern segments must
  equal the input segment count. Capturing is greedy and backtracks, so
  `{product}.csv` captures `product-X` from `product-X.csv`.
- `from = "path"` matches the source-root-relative path; `from = "filename"`
  matches `FileIdentity.Name` only and never touches the filesystem.

## 5. Windows / UNC and root-relative matching

`FileIdentity.Path` is interpreted relative to the per-source root registered
in the extractor (validated to equal the referenced `FileSource` root) using
Go `filepath` semantics. Windows drive and UNC values are ordinary path
values: backslashes are normalized once and all segment work delegates to the
standard `filepath` package. Patterns never contain drive letters or UNC roots
(M-04/M-05/M-06). A file outside the source root is a technical extraction
error, not a silent no-match.

## 6. Propagation pipeline

```text
FileSource.List() -> FileIdentity
  -> MetadataExtractor.Extract() -> Metadata   (before any CSV read)
  -> FileDescriptor
  -> Parser -> Batch { ..., Metadata }
  -> Storage
```

- `FileSource.List` discovers files recursively below `<root>/<date>` (the
  `pattern` is a file-name glob), so nested business layouts are collected and
  then interpreted by path rules.
- `Batch` carries the file-level `Metadata`; every batch of one file carries
  the same metadata (M-12).
- `FileResult` carries the `Metadata` so completion/failure outcomes keep
  business context (M-13).
- Extraction happens only for files that are actually going to be processed:
  already-completed files are skipped before extraction, so rule changes never
  trigger re-collection (M-18).
- A metadata extraction error is a file-level failure: that file is reported
  `Failed` and left incomplete, while other files in the collection proceed
  (M-07/M-10, spec section 30).

## 7. Reload

Metadata configuration reload is Config Reconciliation. Replacing the
`path-metadata` component withdraws the old `MetadataExtractor` provider and
activates the new one; the Collector is rebound by the Runtime Dependency graph
without source changes (M-16). Invalid new configuration is rejected by
`app/config` validation before any Runtime mutation, so the old provider stays
effective (M-17).

## 8. Acceptance coverage

| Test | Where |
| --- | --- |
| M-01 Filename | `app/metadata/metadata_test.go` `TestM01FilenameExtraction` |
| M-02 Single Directory | `TestM02SingleDirectoryExtraction` |
| M-03 Multi-level Directory | `TestM03MultiLevelDirectoryExtraction`, `TestMetadataE2ENestedPathRuleFromRecursiveDiscovery` |
| M-04 Windows | `TestM04WindowsPathExtraction` |
| M-05 UNC | `TestM05UNCPathExtraction` |
| M-06 Root Relative | `TestM06RootRelativePatternHasNoDriveOrRoot` |
| M-07 Required Missing | `TestM07RequiredMissingReturnsError` |
| M-08 Optional Missing | `TestM08OptionalMissingSucceedsWithoutKey`, `TestMetadataE2EOptionalMissingCollects` |
| M-09 Duplicate Key | `TestM09DuplicateKeyRejected`, `TestValidateRejectsMetadataConfigErrors/duplicate key` |
| M-10 Pattern Mismatch | `TestM10PatternMismatchIsExplicit` |
| M-11 Identity Invariance | `app/model/model_test.go` `TestM11MetadataNeverParticipatesInFileIdentity` |
| M-12 Batch Propagation | `TestCollectorMetadataPropagatesToBatchesAndResult`, `TestMetadataE2EFilenameRulePropagatesToBatches` |
| M-13 FileResult Propagation | `TestCollectorMetadataPropagatesToBatchesAndResult` |
| M-14 Dependency | `TestRuntimeIntegrationDependencyActiveCollectionUnloadGone` (metadata Active + Collector) |
| M-15 Plugin Isolation | `plugins/metadata` uses only public `runtime` API; see `BOUNDARY_AUDIT.md` |
| M-16 Reload | `TestMetadataE2EReloadKeepsIdentityAndActivatesNewRules` |
| M-17 Failed Reload | `TestMetadataE2EFailedReloadKeepsOldProvider` |
| M-18 Idempotency | `TestMetadataE2EReloadKeepsIdentityAndActivatesNewRules` |
| M-MULTI-01 Two sources, one extractor | `TestMMulti01TwoSourcesDifferentLayouts`, `TestMMulti05SingleProviderServesTwoSources` |
| M-MULTI-02 Source A reload leaves B | `TestMMulti02ReloadOneSourceLeavesOtherUntouched` |
| M-MULTI-03 Invalid A keeps B + old config | `TestMMulti03InvalidOneSourceDoesNotAffectOthers`, `TestValidateRejectsOneSourceLeavesOtherValid` |
| M-MULTI-04 Identity invariance (multi) | `TestMMulti04ExtractionDoesNotChangeFileIdentity` |
| M-MULTI-05 One capability, one provider | `TestMMulti05SingleProviderServesTwoSources`, `TestCollectorDeclaresSingleMetadataDependency` |

## 9. Boundary gate

`PathMetadataPlugin` can be deleted without affecting GOCORDIS; GOCORDIS keeps
no industrial-domain concept (`runtime.Metadata`, `runtime.MetadataExtractor`,
`extensions/metadata` do not exist there).
