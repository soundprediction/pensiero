# `reasoning` — native ladybug extension for symbolic graph logic

A native ladybug (Kuzu-fork) extension that exposes symbolic, multi-hop graph
reasoning as Cypher-callable table functions over the loaded medical knowledge
graph. It is the in-engine implementation of `../../SYMBOLIC_GRAPH_LOGIC.md`.

## Functions

```cypher
CALL REASON_ENTAILS('fatigue', 'is a symptom of', 'hypothyroidism', 4)
  YIELD verdict, confidence, proof;          -- entailed | contradicted | unsupported

CALL REASON_DERIVE('fatigue', 'hypothyroidism', 4, 0.05)
  YIELD target, confidence, hops, proof;     -- ranked multi-hop proof paths

CALL REASON_CONTRADICTS('patient_cond', 'hyperthyroidism')
  YIELD contradicted, proof;                 -- ontology-disjointness conflict
```

`proof` is a JSON array of `{edge_id, rule, predicate, source, target, confidence}`
steps; `confidence` is the Context-Monoid product with hop decay (see the spec).

## Layout (mirrors the upstream `algo` extension)

```
extension/reasoning/
  CMakeLists.txt                       build_extension_lib(... "reasoning")
  src/main/reasoning_extension.{h,cpp} Extension::load() + extern "C" init()/name()
  src/include/function/reasoning_function.h   function structs (name + getFunctionSet)
  src/function/entails.cpp             TableFunction registration + bind/tableFunc
```

## Building

The extension compiles against the ladybug source headers (the `lbug`-namespaced
Kuzu fork) and links via dynamic lookup against the host `liblbug.so`. It is built
through the ladybug build with the extension on the build list.

1. Place/symlink this directory into a ladybug checkout's `extension/` tree, and add
   `reasoning` to `extension/extension_config.cmake`'s `EXTENSION_LIST`
   (or build out-of-tree pointing `-DBUILD_EXTENSIONS=reasoning` at this path).
2. Build with the precompiled host lib (no need to rebuild all of ladybug):

   ```bash
   # from the ladybug checkout (uses scripts/download-liblbug.sh for the host lib)
   cmake -B build -DBUILD_EXTENSIONS=reasoning \
         -DLBUG_API_USE_PRECOMPILED_LIB=ON -DCMAKE_BUILD_TYPE=Release
   cmake --build build --target reasoning -j
   # -> build/extension/reasoning/libreasoning.lbug_extension
   ```

3. Install the artifact next to the other extensions
   (`lib-ladybug/extensions/libreasoning.lbug_extension`).

## Loading (from go-predicato / humn)

Like the FTS/vector extensions, load it per connection:

```cypher
LOAD EXTENSION 'reasoning';   -- or INSTALL/LOAD per the host's extension manager
```

The humn DDx verifier then calls `CALL REASON_ENTAILS(...)` for in-graph
reasoning, using the returned proof path as the explanation and, on
`contradicted`, hard-failing the claim (see `SYMBOLIC_GRAPH_LOGIC.md` §5).

## Status

Scaffolded: extension class, entry points, function registration, and CMake match
the upstream `algo` extension. **Implementation phase (the `TODO(build)` blocks in
`entails.cpp`)** — anchored bounded reified-path traversal, proof assembly,
confidence composition, and disjointness — is developed against the ladybug build
loop (the `TableFunction` bind/exec API is version-specific and must be
compiled-checked). v1 strategy: run the bounded reified-path query internally via
`ClientContext` and post-process in C++; a later pass moves to direct GDS/storage
frontier traversal for speed.
