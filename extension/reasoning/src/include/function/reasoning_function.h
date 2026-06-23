#pragma once

#include "function/function.h"

// Table-function declarations for the reasoning extension. Each maps to a CALL in
// Cypher and yields a result table. Implementations (bind + table func) live in
// src/function/*.cpp and perform anchored, bounded, reified-aware traversal over
// the storage graph (the predicato medical KG), assembling proof paths + composed
// confidence per SYMBOLIC_GRAPH_LOGIC.md §2-§3.
namespace lbug {
namespace reasoning_extension {

// CALL REASON_ENTAILS(subject STRING, predicate STRING, object STRING, max_hops INT64 := 4,
//                     accepted STRING := '', exclude_deduced BOOL := false)
//   YIELD verdict STRING, confidence DOUBLE, proof STRING
// Decides entailed | contradicted | unsupported for the claim, with the best
// supporting/conflicting proof path (JSON). Passing accepted opts into native
// predicate enforcement; passing exclude_deduced opts into deduced/speculative
// predicate-node quarantine. Legacy arities keep v1 path-existence behavior.
struct EntailsFunction {
    static constexpr const char* name = "REASON_ENTAILS";
    static function::function_set getFunctionSet();
};

// CALL REASON_DERIVE(source STRING, target STRING := '', max_hops INT64 := 4,
//                    min_conf DOUBLE := 0.05, exclude_deduced BOOL := false)
//   YIELD target STRING, confidence DOUBLE, hops INT64, proof STRING
// Ranked multi-hop proof paths from source toward target (or any target).
struct DeriveFunction {
    static constexpr const char* name = "REASON_DERIVE";
    static function::function_set getFunctionSet();
};

// CALL REASON_CONTRADICTS(subject STRING, object STRING)
//   YIELD contradicted BOOL, proof STRING
// Ontology-disjointness conflict check.
struct ContradictsFunction {
    static constexpr const char* name = "REASON_CONTRADICTS";
    static function::function_set getFunctionSet();
};

} // namespace reasoning_extension
} // namespace lbug
