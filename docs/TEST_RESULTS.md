# Test Results and Gate Report

Commands run in this workspace:

```text
go vet ./...
go test ./...
go test -race ./...
```

Sandbox verification used `GOCACHE=/tmp/gocache GOPROXY=off` because the
default Go build cache was read-only and no network was available.

Status: local functional, runtime, UI Composition, boundary, reliability, race,
and resource gates PASS. `npm test`/`npm run build` PASS in this environment;
live desktop Wails/browser E2E needs a display and the Wails toolchain. Live MySQL/PostgreSQL/Oracle databases and a real remote UNC share
were not available, so those adapter/external-service portions are CONDITIONAL
PASS based on code inspection plus the in-process fake `database/sql` driver.

## Unit and plugin coverage

| File | Test | Verifies |
| --- | --- | --- |
| `app/model/model_test.go` | `TestCollectionDateIgnoresTimeZoneForCalendarComparison` | calendar date identity |
| `app/model/model_test.go` | `TestCollectionKeyAndFileIdentityStable` | collection key/file identity |
| `app/model/model_test.go` | `TestM11MetadataNeverParticipatesInFileIdentity` | metadata is excluded from file identity |
| `app/metadata/metadata_test.go` | M-01..M-10 matcher/config acceptance tests | filename/single/multi-directory/Windows/UNC/root-relative/required/optional/duplicate/mismatch |
| `app/metadata/metadata_test.go` | M-MULTI-01..04 engine tests | one extractor serves many sources, reload/invalid isolation, identity, unconfigured source |
| `plugins/metadata/metadata_test.go` | `TestMMulti05SingleProviderServesTwoSources` | one Realm, one MetadataExtractor provider, two source rule sets |
| `plugins/collector/collector_test.go` | `TestCollectorDeclaresSingleMetadataDependency` | Collector depends on one MetadataExtractor capability |
| `app/date/policy_test.go` | `TestYesterdayPolicy`, `TestSpecificPolicy`, `TestUnknownPolicyRejected` | date policies |
| `app/errs/errors_test.go` | `TestClassifySourceError` | source error classes |
| `app/parser/parser_test.go` | `TestParseHeadersQuotesCommasAndLineEndings`, `TestParseWithoutHeader`, `TestParseMalformed` | streaming CSV parsing |
| `app/source/source_test.go` | `TestListReadStableFile`, `TestMissingDateDirectoryClassified`, `TestStableWindowSkipsNewFile`, `TestListRecursiveNestedDirectories`, `TestListRecursiveRespectsStableWindowAndPattern` | file discovery/stability/classification/recursion |
| `app/state/state_test.go` | `TestMemoryStateClaimCompleteAndFileIdempotency`, `TestMemoryStateFailedCanRetryAndListIncomplete`, `TestFileStatePersistsAcrossRestart` | claim/retry/persistence |
| `app/storage/memory_test.go` | `TestMemoryStoreIdempotent`, `TestMemoryStoreBatchFailureIsRetryable` | idempotent batch writes |
| `app/storage/sql_test.go` | `TestSQLStoreOpenWriteCloseForDialects` | MySQL/PostgreSQL/Oracle open/write/close SQL paths against a fake driver |
| `app/recovery/planner_test.go` | `TestPlannerFindsKnownAndCalendarGaps` | known rows plus calendar gap planning |
| `app/config/validate_test.go` | `TestValidateAcceptCompleteConfig`, `TestValidateRejectsMissingReference`, `TestValidateRejectsWrongReferenceKind`, `TestValidateRejectsBadScheduleAndBatch`, `TestValidateAcceptsDefaultedAndRejectsInvalidNumericValues`, `TestValidateAcceptsMultipleSourcesWithOwnRules`, `TestValidateRejectsOneSourceLeavesOtherValid` | config validation before Runtime mutation, multi-source metadata |
| `app/collector/executor_test.go` | `TestCollectorStoresFilesAndIsIdempotent`, `TestCollectorPartialFileFailureSkipsCompletedFiles`, `TestMissingDirectoryStaysPending`, `TestCollectorStorageFailureIsolation` | executor semantics |
| `app/collector/executor_test.go` | `TestCollectorMetadataPropagatesToBatchesAndResult`, `TestCollectorMetadataErrorIsFileLevelFailure` | metadata to Batch/FileResult propagation and error isolation |

## E2E and runtime coverage

| File | Test | Verifies |
| --- | --- | --- |
| `tests/e2e_test.go` | `TestCSVE2E01LocalToCSVToStorage` | local source -> parser -> storage |
| `tests/e2e_test.go` | `TestCSVE2E02UNCTypeSameCollector` | UNC-typed source as ordinary path value |
| `tests/e2e_test.go` | `TestCSVE2E03YesterdayPolicy` | today -> yesterday collection |
| `tests/e2e_test.go` | `TestCSVE2E04RestartRecovery` | persistent state and restart recovery |
| `tests/e2e_test.go` | `TestCSVE2E05RepeatRunNoDuplicate` | repeated execution stays idempotent |
| `tests/e2e_test.go` | `TestCSVE2E06MultipleFilesAllSucceed` | multi-file success |
| `tests/e2e_test.go` | `TestCSVE2E07PartialFileRetry` | A skip / B retry / C skip |
| `tests/e2e_test.go` | `TestCSVE2E08StorageReplacementCollectorUnchangedSemantics` | storage replacement without Collector changes |
| `tests/e2e_test.go` | `TestCSVE2E09SourceReplacement` | source replacement without Collector changes |
| `tests/e2e_test.go` | `TestCSVE2E10ConfigReconciliationNoOpAndReplace` | Config Controller no-op and replace convergence |
| `tests/e2e_scheduler_test.go` | `TestCSVE2E11SchedulerEventCollector` | scheduler -> event -> collector |
| `tests/e2e_metadata_test.go` | MetadataE2E filename/path/reload/failed-reload/optional suite | M-12/M-16/M-17/M-18 end-to-end propagation and reconciliation |
| `tests/config_smoke_test.go` | `TestSampleConfigsParseAndValidate` | shipped TOML examples parse and validate |
| `app/query/query_test.go` | read model/observation unit tests | views from events, failures, subscribe/publish/feed |
| `app/ui/composition_test.go` | P3-01..P3-11 registry tests | register/unregister ownership, duplicates, deterministic ordering, reload, change notification |
| `tests/e2e_ui_test.go` | `TestUIE2EQueryObservationCommandLoop`, `TestUIE2EPluginIsolation` | UI Command -> Event -> Collector -> Query -> Observation -> UI view; UI unload isolation |
| `tests/e2e_ui_p2_test.go` | `TestUIP2HostBridgeFullLoop`, `TestUIP2ErrorBoundary`, `TestUIP2Isolation` | Wails/React host bridge: Query DTOs, Observation listener, Command, error boundary, isolation |
| `tests/e2e_p21_test.go` | `TestP21ProductionSinkAndAsyncCommand` | production ObservationSink, async command acceptance, unload releases sink, Collector isolation |
| `frontend` | `npm run build`, `npm test` | PASS in this environment (tsc+vite; vitest 2 tests); desktop E2E needs GUI/Wails toolchain |
| `tests/e2e_ui_p3_test.go` | `TestP3IndependentPluginLoadUnloadReload`, `TestP3MultiPluginCompositionAndHTTPDto` | independent contributor load/unload/reload, multi-plugin composition, observation payload, adapter DTOs |
| `internal/webui` | `TestWebUIHTTPBridge` | embedded web UI: static index, /api trigger+collections+files+metadata, UI composition DTOs, SSE observation |
| `tests/e2e_test.go` | `TestCSVE2E12RuntimeCloseAllGone` | close leaves no owned components |
| `tests/e2e_test.go` | `TestRuntimeIntegrationDependencyActiveCollectionUnloadGone` | config -> component -> dependency -> active -> collection -> gone |

## Gates

| Gate | Result | Evidence |
| --- | --- | --- |
| Functional | CONDITIONAL PASS | local/memory paths PASS; SQL dialects run through a fake driver; real MySQL/PostgreSQL/Oracle services and a remote UNC share were unavailable |
| Runtime | PASS | component/fiber/activation/dependency/effect/event reconciliation tests above |
| Boundary | PASS | no domain concept added under `runtime/`; see BOUNDARY_AUDIT.md |
| Reliability | PASS | exercised restart/missing/partial/storage/repeat/close paths |
| Race | PASS | `go test -race ./...` |
| Resource | PASS | exercised code-owned resources; see RESOURCE_OWNERSHIP_AUDIT.md |
