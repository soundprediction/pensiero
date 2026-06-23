#pragma once

#include "extension/extension.h"

// ReasoningExtension is a native ladybug (Kuzu-fork) extension that exposes
// symbolic graph reasoning as Cypher-callable table functions over the loaded
// medical knowledge graph. It is the in-engine implementation of
// SYMBOLIC_GRAPH_LOGIC.md (bounded, anchored, reified-aware multi-hop derivation
// with proof paths and Context-Monoid confidence).
//
// Registered functions (see reasoning_function.h):
//   CALL REASON_ENTAILS(subject, predicate, object [, max_hops [, accepted [, exclude_deduced]]])
//        YIELD verdict, confidence, proof
//   CALL REASON_DERIVE(source, target [, max_hops [, min_conf [, exclude_deduced]]])
//        YIELD target, confidence, hops, proof
//   CALL REASON_CONTRADICTS(subject, object) YIELD contradicted, proof
namespace lbug {
namespace reasoning_extension {

class ReasoningExtension final : public extension::Extension {
public:
    static constexpr char EXTENSION_NAME[] = "REASONING";

public:
    static void load(main::ClientContext* context);
};

} // namespace reasoning_extension
} // namespace lbug
