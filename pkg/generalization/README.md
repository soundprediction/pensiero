# Generalization Builder

`pkg/generalization` builds a small per-scope graph from a larger predicato graph.
The output keeps scoped entities, selected ontological parent nodes, reified
`is_a` links, retained direct relations, and parent-level lifted relations whose
child support meets the configured threshold.

The package is pure Go. Reads use `reasoning.GraphQuerier`, and predicate behavior
comes from the caller-supplied `reasoning.PredicateRegistry`; nil registries use
`reasoning.DefaultGeneralRegistry`.

## Ladybug Publishing

The Phase 1 command writes each output graph to a fresh temporary Ladybug path and
renames that completed path into place. Readers should only open the published
path, so they never observe partially-created tables.

Ladybug v0.17.0 exposes `ReadOnly` in `SystemConfig`, but same-path read-write
and read-only co-open behavior is runtime-environment sensitive. Later long-running
writers should probe that mode at startup; if the probe fails, continue using the
snapshot-publish pattern above.

## IGL Service

`pensiero serve` runs the Inductive Generalization Loop for one or more scopes:

```sh
pensiero serve \
  --source source.ladybug \
  --scopes alpha,beta \
  --out-dir generalization-graphs \
  --interval 1m
```

`--once` performs one pass and exits:

```sh
pensiero serve --source source.ladybug --scopes alpha --out-dir generalization-graphs --once
```

The service accepts the same core builder knobs as `build-generalization`:
`--min-support`, `--min-parent-support`, `--max-parent-level`, `--predicates`,
`--taxonomic-predicates`, `--taxonomic-direction`, and `--registry`.
Environment defaults are available as
`PENSIERO_SOURCE`, `PENSIERO_SCOPES`, `PENSIERO_SCOPES_DIR`,
`PENSIERO_OUT_DIR`, `PENSIERO_INTERVAL`, `PENSIERO_MIN_SUPPORT`,
`PENSIERO_MIN_PARENT_SUPPORT`, `PENSIERO_MAX_PARENT_LEVEL`,
`PENSIERO_PREDICATES`, `PENSIERO_TAXONOMIC_PREDICATES`,
`PENSIERO_TAXONOMIC_DIRECTION`, `PENSIERO_REGISTRY`, and
`PENSIERO_HEALTH_ADDR`.

`--scopes-dir` points at JSON descriptor files. A descriptor may override the
global builder knobs for that scope:

```json
{
  "name": "alpha",
  "scope": "alpha",
  "scope_entities": ["A", "B"],
  "scope_entities_file": "alpha.entities",
  "predicates": ["R"],
  "taxonomic_predicates": ["is_a"],
  "taxonomic_direction": "child-to-parent",
  "min_support": 2,
  "min_parent_support": 1,
  "max_parent_level": 4
}
```

Each pass rebuilds a scope through `Build`, writes a fresh temporary snapshot,
opens and validates that snapshot, then publishes it as:

```text
<out-dir>/<scope>.g_g.ladybug
```

Publishing is temp plus rename. Readers open only the published path read-only;
they either see the previous complete snapshot or the next complete snapshot.
There are no in-place writes to the published graph. If the underlying graph path
is a directory, the published path is an atomically replaced symlink to a complete
version directory.

The service is the sole writer for these outputs. Other processes should not
write to `*.g_g.ladybug`; they should open the published path read-only and run
their own queries locally. The service exposes only `/healthz` and `/metrics`
JSON endpoints, reporting last pass time, per-scope counts, deltas, and last
errors. It does not expose a query API.

The current loop performs a full rebuild on each tick. The rebuild is isolated in
`Publisher.Publish`, so a later incremental selector can replace the build step
without changing the publish contract.
