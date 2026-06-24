# gRPC Server Mode

Pensiero's symbolic reasoner can be served over **gRPC** so reasoning runs in a
separate process — or on a different machine — from its host (e.g. humn), and
scales independently as a pool.

The contract mirrors the `reasoning.Reasoner` interface, so the gRPC client is a
drop-in for an in-process reasoner.

## What it exposes

The service `pensiero.v1.ReasonerService` (`proto/pensiero/v1/reasoning.proto`):

| RPC | Maps to |
|-----|---------|
| `Entails` | `Reasoner.Entails(Claim) → EntailResult` |
| `Contradicts` | `Reasoner.Contradicts(Claim) → (bool, *Proof)` |
| `Derive` | `Reasoner.Derive(DeriveRequest) → []Proof` |
| `Health` | liveness probe (LB readiness) |

Messages mirror `reasoning.Claim` / `Proof` / `ProofStep` / `EntailResult`.

## Serve a reasoner

The reasoner runs over a graph, so the host constructs it (load the
topic/generalization graph + predicate registry, build a `NativeReasoner`) and
hands it to the gRPC server helper:

```go
import grpcsvc "github.com/soundprediction/pensiero/pkg/grpcsvc"

reasoner := reasoning.NewNativeReasoner(graph, registry, cfg) // host builds this
// one-call serve, or NewServer(reasoner).Register(grpcServer) for a shared server:
log.Fatal(grpcsvc.Serve(reasoner, ":50072"))
```

For a pool, run several such servers (each over the same read-only graph —
reasoning is a pure query, so instances are interchangeable) behind a DNS /
headless name.

> A standalone `serve-grpc` CLI that builds the reasoner from a graph path is a
> small follow-up; today the reasoner is constructed by the host application
> (humn's `TopicGraphReasoner` is the reference wiring).

## Use it from Go

```go
cli, _ := grpcsvc.Dial("reasoner-host:50072")        // see Pools below
defer cli.Close()
res, _ := cli.Entails(ctx, reasoning.Claim{Subject: "hypothyroidism", Predicate: "is_a", Object: "thyroid disorder"})
```

`grpcsvc.Client` implements `reasoning.Reasoner` (compile-time checked), so it
substitutes for an in-process reasoner anywhere that interface is accepted.

## Pools (horizontal scaling)

`Dial` load-balances (round-robin) across a **pool** of reasoner servers. The
target may be a single `host:port`, a comma-separated static pool
`h1:50072,h2:50072`, or a scheme target like `dns:///pensiero.svc:50072` — a
dynamic pool the resolver re-resolves as instances autoscale. (`Dial` adds the
`round_robin` service config; gRPC's default `pick_first` would pin to one.)

Reasoner instances are **stateless given a shared read-only graph**, so pool
freely; each instance opens the same generalization/topic graph (read replicas /
shared read-only volume).

For `pensiero serve` pools that also run IGL, run N identical daemons behind the
same gRPC target, for example `dns:///pensiero.svc:50072`. In shared snapshot
mode, give every daemon the same `--out-dir`; the default `--leader-mode=flock`
takes a POSIX `flock` file per scope in that directory, so exactly one daemon
optimizes and publishes each `<scope>.g_g.ladybug` while the other daemons keep
serving and hot-reload the published generation. Local snapshot mode is still
available by giving each daemon its own `--out-dir`, or by using
`--leader-mode=none` to preserve the single-instance behavior where every
daemon leads every scope. `--leader-mode=k8s-lease` is reserved for a future
Kubernetes Lease implementation.

## Regenerating the stubs

```bash
# requires protoc, protoc-gen-go, protoc-gen-go-grpc on PATH
./proto/generate.sh
```

## Wiring from humn

```toml
[agent.reasoning]
grpc_endpoint = "dns:///pensiero.svc:50072"   # pool; or "host:50072", or "h1:port,h2:port"
```

When set, humn routes DDx logical reasoning to the remote pool instead of the
in-process topic-graph reasoner.
