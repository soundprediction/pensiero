#include "function/reasoning_function.h"

#include "function/table/bind_data.h"
#include "function/table/table_function.h"
#include "main/client_context.h"

using namespace lbug::common;
using namespace lbug::function;

namespace lbug {
namespace reasoning_extension {

// NOTE (implementation phase — requires the ladybug build loop):
// Each function below follows the TableFunction registration shape used by the
// `algo` extension (bindFunc defines columns + parses args; tableFunc emits rows).
// The reasoning v1 strategy is to run the bounded, anchored, reified-aware path
// query INTERNALLY via the ClientContext's query engine (reusing the engine's
// path matching) and assemble the proof path + Context-Monoid confidence in C++,
// per SYMBOLIC_GRAPH_LOGIC.md §2-§3. A later pass can replace the internal query
// with direct GDS/storage frontier traversal for speed.

// ---- REASON_ENTAILS(subject, predicate, object [, max_hops]) ----
// YIELD verdict STRING, confidence DOUBLE, proof STRING

static std::unique_ptr<TableFuncBindData> entailsBindFunc(main::ClientContext* /*context*/,
    const TableFuncBindInput* input) {
    // args: subject, predicate, object, [max_hops]
    // columns: verdict (STRING), confidence (DOUBLE), proof (STRING)
    std::vector<LogicalType> columnTypes;
    std::vector<std::string> columnNames;
    columnNames.emplace_back("verdict");
    columnTypes.emplace_back(LogicalType::STRING());
    columnNames.emplace_back("confidence");
    columnTypes.emplace_back(LogicalType::DOUBLE());
    columnNames.emplace_back("proof");
    columnTypes.emplace_back(LogicalType::STRING());
    // TODO(build): read input->params -> subject/predicate/object/max_hops into bind data.
    (void)input;
    return TableFunction::bindFuncDefinition(std::move(columnNames), std::move(columnTypes));
}

static offset_t entailsTableFunc(const TableFuncInput& /*input*/, TableFuncOutput& /*output*/) {
    // TODO(build):
    //  1. resolve subject/object Entity offsets (PK index lookup, anchored).
    //  2. normalize predicate via the predicate registry (functor F).
    //  3. run bounded reified path search (*2..2*max_hops) subject->object, dropping
    //     molecular intermediates, collecting RelatesToNode_ predicate chain.
    //  4. check disjointness (OntologyDisjoint) over the subject's is_a* classes; a
    //     conflict -> verdict='contradicted' (hard fail).
    //  5. else if a path exists -> verdict='entailed' with composed confidence
    //     (prod(edge) * decay^(hops-1)); else 'unsupported'.
    //  6. emit one row (verdict, confidence, proof-JSON).
    return 0; // rows produced
}

function_set EntailsFunction::getFunctionSet() {
    function_set result;
    auto func = std::make_unique<TableFunction>(EntailsFunction::name,
        std::vector<LogicalTypeID>{LogicalTypeID::STRING, LogicalTypeID::STRING,
            LogicalTypeID::STRING});
    func->bindFunc = entailsBindFunc;
    func->tableFunc = entailsTableFunc;
    func->initSharedStateFunc = TableFunction::initSharedState;
    func->initLocalStateFunc = TableFunction::initEmptyLocalState;
    func->canParallelFunc = [] { return false; };
    result.push_back(std::move(func));
    return result;
}

// ---- REASON_DERIVE(source, target [, max_hops [, min_conf]]) ----
static std::unique_ptr<TableFuncBindData> deriveBindFunc(main::ClientContext*,
    const TableFuncBindInput* input) {
    std::vector<LogicalType> columnTypes;
    std::vector<std::string> columnNames;
    columnNames.emplace_back("target");
    columnTypes.emplace_back(LogicalType::STRING());
    columnNames.emplace_back("confidence");
    columnTypes.emplace_back(LogicalType::DOUBLE());
    columnNames.emplace_back("hops");
    columnTypes.emplace_back(LogicalType::INT64());
    columnNames.emplace_back("proof");
    columnTypes.emplace_back(LogicalType::STRING());
    (void)input;
    return TableFunction::bindFuncDefinition(std::move(columnNames), std::move(columnTypes));
}

static offset_t deriveTableFunc(const TableFuncInput&, TableFuncOutput&) {
    // TODO(build): ranked multi-hop proof paths source->target (or any target).
    return 0;
}

function_set DeriveFunction::getFunctionSet() {
    function_set result;
    auto func = std::make_unique<TableFunction>(DeriveFunction::name,
        std::vector<LogicalTypeID>{LogicalTypeID::STRING, LogicalTypeID::STRING});
    func->bindFunc = deriveBindFunc;
    func->tableFunc = deriveTableFunc;
    func->initSharedStateFunc = TableFunction::initSharedState;
    func->initLocalStateFunc = TableFunction::initEmptyLocalState;
    func->canParallelFunc = [] { return false; };
    result.push_back(std::move(func));
    return result;
}

// ---- REASON_CONTRADICTS(subject, object) ----
static std::unique_ptr<TableFuncBindData> contradictsBindFunc(main::ClientContext*,
    const TableFuncBindInput* input) {
    std::vector<LogicalType> columnTypes;
    std::vector<std::string> columnNames;
    columnNames.emplace_back("contradicted");
    columnTypes.emplace_back(LogicalType::BOOL());
    columnNames.emplace_back("proof");
    columnTypes.emplace_back(LogicalType::STRING());
    (void)input;
    return TableFunction::bindFuncDefinition(std::move(columnNames), std::move(columnTypes));
}

static offset_t contradictsTableFunc(const TableFuncInput&, TableFuncOutput&) {
    // TODO(build): is_a* membership of subject vs OntologyDisjoint(., object).
    return 0;
}

function_set ContradictsFunction::getFunctionSet() {
    function_set result;
    auto func = std::make_unique<TableFunction>(ContradictsFunction::name,
        std::vector<LogicalTypeID>{LogicalTypeID::STRING, LogicalTypeID::STRING});
    func->bindFunc = contradictsBindFunc;
    func->tableFunc = contradictsTableFunc;
    func->initSharedStateFunc = TableFunction::initSharedState;
    func->initLocalStateFunc = TableFunction::initEmptyLocalState;
    func->canParallelFunc = [] { return false; };
    result.push_back(std::move(func));
    return result;
}

} // namespace reasoning_extension
} // namespace lbug
