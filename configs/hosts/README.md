# Host configurations — the fleet inventory

One TOML per host, named after the host. This directory is the fleet's
desired-state ledger: git history answers "which host composes which
plugins, since when, and why". Each host runs the same binary with its own
config file:

    csv-collector -config hosts/csv-line-a.toml        # resident (watcher/scheduler)
    csv-collector -config hosts/csv-line-a.toml -once  # scheduled pass

Conventions:

- `source_id` is prefixed with the host/line (`gauge-l1`, `csv-line-a`) so
  rows converging in a shared database never collide.
- The `ui` component's `host_id` must match the file name — it is what the
  console reports at `/api/meta` and what the Fleet page displays.
- `fleet_peers` (flat key in the ui component config) lists sibling consoles; when set, the console contributes a
  Fleet page aggregating every peer's meta (observation-only).

Deployment states visible in the console: Active (installed + running),
Gone (installed + deactivated). Removing a component from its config file
is the uninstall path; reconciliation applies it on the next start/reload.
