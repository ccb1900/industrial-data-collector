# Resource Ownership Audit

Status: PASS for exercised paths; real SQL drivers require external service
verification.

| Resource | Owner | Release |
| --- | --- | --- |
| CSV file handle | Collector Executor per file | `defer rc.Close`; worker cancellation returns from execution |
| Collector worker goroutine | Collector activation Effect | cleanup cancels context and waits on worker done |
| CollectionRequested handler | Collector activation Effect | `runtime.On` inverse |
| Scheduler extension timer/goroutines | `plugins/scheduler` activation Effect | runtime scheduler extension `Close` |
| SQL DB handle | Storage activation | cleanup calls `Storage.Close` |
| FileState snapshot file | State component | no long-lived handle; atomic rename per mutation |
| FileWatcher/Adapter | WatchHost | `adapter.CloseContext` + watcher close before Runtime close |

No global collector, database connection, scheduler, event bus, or file
registry exists. Each long-lived resource has one owning Component activation.
