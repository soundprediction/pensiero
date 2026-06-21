#include "function/reasoning_function.h"

#include <algorithm>
#include <cctype>
#include <cmath>
#include <sstream>

#include "binder/binder.h"
#include "common/types/value/nested.h"
#include "common/types/value/value.h"
#include "function/table/bind_data.h"
#include "function/table/bind_input.h"
#include "function/table/simple_table_function.h"
#include "main/client_context.h"
#include "main/connection.h"
#include "main/database.h"
#include "main/query_result.h"
#include "processor/result/flat_tuple.h"

using namespace lbug::common;
using namespace lbug::function;
using namespace lbug::main;

namespace lbug {
namespace reasoning_extension {

// ---------------------------------------------------------------------------
// Shared result-row model. Each reasoning function computes its rows in bindFunc
// (where a ClientContext is available to run the internal bounded reified-path
// query) and stashes them in a TableFuncBindData subclass; the SimpleTableFunc
// internal tableFunc then streams them out vector-by-vector.
// ---------------------------------------------------------------------------

struct ReasonRow {
    // entails / contradicts / derive share a superset of fields; each function
    // only populates + emits the columns it declares.
    std::string verdict;     // entailed | contradicted | unsupported
    std::string target;      // derive: reached entity
    std::string proof;       // JSON array of proof steps
    double confidence = 0.0;
    int64_t hops = 0;
    bool contradicted = false;
};

// ===========================================================================
// REASON_ENTAILS(subject, predicate, object [, max_hops])
//   YIELD verdict STRING, confidence DOUBLE, proof STRING
// ===========================================================================

struct EntailsBindData final : TableFuncBindData {
    std::vector<ReasonRow> rows;

    EntailsBindData(std::vector<ReasonRow> rows, binder::expression_vector columns,
        row_idx_t numRows)
        : TableFuncBindData{std::move(columns), numRows}, rows{std::move(rows)} {}

    std::unique_ptr<TableFuncBindData> copy() const override {
        return std::make_unique<EntailsBindData>(rows, columns, numRows);
    }
};

static offset_t entailsTableFunc(const TableFuncMorsel& morsel, const TableFuncInput& input,
    DataChunk& output) {
    const auto& rows = input.bindData->constPtrCast<EntailsBindData>()->rows;
    const auto n = morsel.endOffset - morsel.startOffset;
    for (auto i = 0u; i < n; i++) {
        const auto& r = rows[morsel.startOffset + i];
        output.getValueVectorMutable(0).setValue(i, r.verdict);
        output.getValueVectorMutable(1).setValue(i, r.confidence);
        output.getValueVectorMutable(2).setValue(i, r.proof);
    }
    return n;
}

// Forward declaration of the real reasoning core (implemented further below).
std::vector<ReasonRow> runEntails(ClientContext* context, const std::string& subject,
    const std::string& predicate, const std::string& object, int64_t maxHops);

static std::unique_ptr<TableFuncBindData> entailsBindFunc(ClientContext* context,
    const TableFuncBindInput* input) {
    auto subject = input->getLiteralVal<std::string>(0);
    auto predicate = input->getLiteralVal<std::string>(1);
    auto object = input->getLiteralVal<std::string>(2);
    int64_t maxHops = 4;
    if (input->params.size() > 3) {
        maxHops = input->getLiteralVal<int64_t>(3);
    }

    std::vector<std::string> columnNames;
    std::vector<LogicalType> columnTypes;
    columnNames.emplace_back("verdict");
    columnTypes.emplace_back(LogicalType::STRING());
    columnNames.emplace_back("confidence");
    columnTypes.emplace_back(LogicalType::DOUBLE());
    columnNames.emplace_back("proof");
    columnTypes.emplace_back(LogicalType::STRING());
    columnNames = TableFunction::extractYieldVariables(columnNames, input->yieldVariables);
    auto columns = input->binder->createVariables(columnNames, columnTypes);

    auto rows = runEntails(context, subject, predicate, object, maxHops);
    auto numRows = rows.size();
    return std::make_unique<EntailsBindData>(std::move(rows), std::move(columns), numRows);
}

function_set EntailsFunction::getFunctionSet() {
    function_set result;
    // Two arities: (subject, predicate, object) and (..., max_hops INT64).
    std::vector<std::vector<LogicalTypeID>> sigs = {
        {LogicalTypeID::STRING, LogicalTypeID::STRING, LogicalTypeID::STRING},
        {LogicalTypeID::STRING, LogicalTypeID::STRING, LogicalTypeID::STRING,
            LogicalTypeID::INT64}};
    for (auto& sig : sigs) {
        auto func = std::make_unique<TableFunction>(EntailsFunction::name, sig);
        func->bindFunc = entailsBindFunc;
        func->tableFunc = SimpleTableFunc::getTableFunc(entailsTableFunc);
        func->initSharedStateFunc = SimpleTableFunc::initSharedState;
        func->initLocalStateFunc = TableFunction::initEmptyLocalState;
        func->canParallelFunc = [] { return false; };
        result.push_back(std::move(func));
    }
    return result;
}

// ===========================================================================
// REASON_DERIVE(source, target [, max_hops [, min_conf]])
//   YIELD target STRING, confidence DOUBLE, hops INT64, proof STRING
// ===========================================================================

struct DeriveBindData final : TableFuncBindData {
    std::vector<ReasonRow> rows;

    DeriveBindData(std::vector<ReasonRow> rows, binder::expression_vector columns, row_idx_t numRows)
        : TableFuncBindData{std::move(columns), numRows}, rows{std::move(rows)} {}

    std::unique_ptr<TableFuncBindData> copy() const override {
        return std::make_unique<DeriveBindData>(rows, columns, numRows);
    }
};

static offset_t deriveTableFunc(const TableFuncMorsel& morsel, const TableFuncInput& input,
    DataChunk& output) {
    const auto& rows = input.bindData->constPtrCast<DeriveBindData>()->rows;
    const auto n = morsel.endOffset - morsel.startOffset;
    for (auto i = 0u; i < n; i++) {
        const auto& r = rows[morsel.startOffset + i];
        output.getValueVectorMutable(0).setValue(i, r.target);
        output.getValueVectorMutable(1).setValue(i, r.confidence);
        output.getValueVectorMutable(2).setValue(i, r.hops);
        output.getValueVectorMutable(3).setValue(i, r.proof);
    }
    return n;
}

std::vector<ReasonRow> runDerive(ClientContext* context, const std::string& source,
    const std::string& target, int64_t maxHops, double minConf, int64_t limit);

static std::unique_ptr<TableFuncBindData> deriveBindFunc(ClientContext* context,
    const TableFuncBindInput* input) {
    auto source = input->getLiteralVal<std::string>(0);
    std::string target;
    if (input->params.size() > 1) {
        target = input->getLiteralVal<std::string>(1);
    }
    int64_t maxHops = 4;
    if (input->params.size() > 2) {
        maxHops = input->getLiteralVal<int64_t>(2);
    }
    double minConf = 0.05;
    if (input->params.size() > 3) {
        // NOTE: TableFuncBindInput::getLiteralVal<double> is not exported by the
        // 0.17.0 host lib; read the literal Value and extract the double from it.
        minConf = input->getValue(3).getValue<double>();
    }

    std::vector<std::string> columnNames;
    std::vector<LogicalType> columnTypes;
    columnNames.emplace_back("target");
    columnTypes.emplace_back(LogicalType::STRING());
    columnNames.emplace_back("confidence");
    columnTypes.emplace_back(LogicalType::DOUBLE());
    columnNames.emplace_back("hops");
    columnTypes.emplace_back(LogicalType::INT64());
    columnNames.emplace_back("proof");
    columnTypes.emplace_back(LogicalType::STRING());
    columnNames = TableFunction::extractYieldVariables(columnNames, input->yieldVariables);
    auto columns = input->binder->createVariables(columnNames, columnTypes);

    auto rows = runDerive(context, source, target, maxHops, minConf, 8 /* limit */);
    auto numRows = rows.size();
    return std::make_unique<DeriveBindData>(std::move(rows), std::move(columns), numRows);
}

function_set DeriveFunction::getFunctionSet() {
    function_set result;
    // Arities: (source, target), (..., max_hops), (..., max_hops, min_conf).
    std::vector<std::vector<LogicalTypeID>> sigs = {
        {LogicalTypeID::STRING, LogicalTypeID::STRING},
        {LogicalTypeID::STRING, LogicalTypeID::STRING, LogicalTypeID::INT64},
        {LogicalTypeID::STRING, LogicalTypeID::STRING, LogicalTypeID::INT64,
            LogicalTypeID::DOUBLE}};
    for (auto& sig : sigs) {
        auto func = std::make_unique<TableFunction>(DeriveFunction::name, sig);
        func->bindFunc = deriveBindFunc;
        func->tableFunc = SimpleTableFunc::getTableFunc(deriveTableFunc);
        func->initSharedStateFunc = SimpleTableFunc::initSharedState;
        func->initLocalStateFunc = TableFunction::initEmptyLocalState;
        func->canParallelFunc = [] { return false; };
        result.push_back(std::move(func));
    }
    return result;
}

// ===========================================================================
// REASON_CONTRADICTS(subject, object)
//   YIELD contradicted BOOL, proof STRING
// ===========================================================================

struct ContradictsBindData final : TableFuncBindData {
    std::vector<ReasonRow> rows;

    ContradictsBindData(std::vector<ReasonRow> rows, binder::expression_vector columns,
        row_idx_t numRows)
        : TableFuncBindData{std::move(columns), numRows}, rows{std::move(rows)} {}

    std::unique_ptr<TableFuncBindData> copy() const override {
        return std::make_unique<ContradictsBindData>(rows, columns, numRows);
    }
};

static offset_t contradictsTableFunc(const TableFuncMorsel& morsel, const TableFuncInput& input,
    DataChunk& output) {
    const auto& rows = input.bindData->constPtrCast<ContradictsBindData>()->rows;
    const auto n = morsel.endOffset - morsel.startOffset;
    for (auto i = 0u; i < n; i++) {
        const auto& r = rows[morsel.startOffset + i];
        output.getValueVectorMutable(0).setValue(i, r.contradicted);
        output.getValueVectorMutable(1).setValue(i, r.proof);
    }
    return n;
}

std::vector<ReasonRow> runContradicts(ClientContext* context, const std::string& subject,
    const std::string& object);

static std::unique_ptr<TableFuncBindData> contradictsBindFunc(ClientContext* context,
    const TableFuncBindInput* input) {
    auto subject = input->getLiteralVal<std::string>(0);
    auto object = input->getLiteralVal<std::string>(1);

    std::vector<std::string> columnNames;
    std::vector<LogicalType> columnTypes;
    columnNames.emplace_back("contradicted");
    columnTypes.emplace_back(LogicalType::BOOL());
    columnNames.emplace_back("proof");
    columnTypes.emplace_back(LogicalType::STRING());
    columnNames = TableFunction::extractYieldVariables(columnNames, input->yieldVariables);
    auto columns = input->binder->createVariables(columnNames, columnTypes);

    auto rows = runContradicts(context, subject, object);
    auto numRows = rows.size();
    return std::make_unique<ContradictsBindData>(std::move(rows), std::move(columns), numRows);
}

function_set ContradictsFunction::getFunctionSet() {
    function_set result;
    auto func = std::make_unique<TableFunction>(ContradictsFunction::name,
        std::vector<LogicalTypeID>{LogicalTypeID::STRING, LogicalTypeID::STRING});
    func->bindFunc = contradictsBindFunc;
    func->tableFunc = SimpleTableFunc::getTableFunc(contradictsTableFunc);
    func->initSharedStateFunc = SimpleTableFunc::initSharedState;
    func->initLocalStateFunc = TableFunction::initEmptyLocalState;
    func->canParallelFunc = [] { return false; };
    result.push_back(std::move(func));
    return result;
}

// ===========================================================================
// Reasoning core (v1): anchored, bounded, reified-aware path traversal.
//
// The medical KG uses the predicato reified-edge model
//   (a:Entity)-[:RELATES_TO]->(rn:RelatesToNode_)-[:RELATES_TO]->(b:Entity)
// so one *logical* hop is two physical RELATES_TO edges and the predicate is on
// the intermediate RelatesToNode_ (SYMBOLIC_GRAPH_LOGIC.md §1.2/§2.1).
//
// Strategy: run the anchored, bounded reified-path query INTERNALLY via the
// ClientContext query engine (reusing its variable-length path matching), then
// assemble the proof + Context-Monoid confidence in C++ (§2.1, §3).
// ===========================================================================

namespace {

constexpr double kDecay = 0.9;       // hop decay (§3.2)
constexpr double kEdgePrior = 0.8;   // per-edge prior (RelatesToNode_ carries no confidence col)

// Functor F (§1.3/§2.6): normalize a raw graph predicate to a canonical label.
// Lowercase + spaces->underscores; identity otherwise.
std::string normalizePredicate(const std::string& raw) {
    std::string s;
    s.reserve(raw.size());
    for (char c : raw) {
        if (c == ' ' || c == '-') {
            s.push_back('_');
        } else {
            s.push_back(static_cast<char>(std::tolower(static_cast<unsigned char>(c))));
        }
    }
    return s;
}

// Escape a string for embedding in JSON.
std::string jsonEscape(const std::string& s) {
    std::string out;
    out.reserve(s.size() + 8);
    for (char c : s) {
        switch (c) {
        case '"':
            out += "\\\"";
            break;
        case '\\':
            out += "\\\\";
            break;
        case '\n':
            out += "\\n";
            break;
        case '\r':
            out += "\\r";
            break;
        case '\t':
            out += "\\t";
            break;
        default:
            out.push_back(c);
        }
    }
    return out;
}

// Escape a string literal for embedding in a Cypher query (single-quoted).
std::string cypherEscape(const std::string& s) {
    std::string out;
    out.reserve(s.size() + 4);
    for (char c : s) {
        if (c == '\'' || c == '\\') {
            out.push_back('\\');
        }
        out.push_back(c);
    }
    return out;
}

// A resolved logical path: the ordered RelatesToNode_ predicates + uuids, and the
// ordered Entity names along the path (source ... object).
struct LogicalPath {
    std::vector<std::string> predicates;  // one per logical hop (RelatesToNode_.name)
    std::vector<std::string> edgeIds;     // RelatesToNode_.uuid per hop
    std::vector<std::string> entityNames; // Entity names: source, mids..., object
    int64_t hops = 0;
};

// Read a STRING[] Value into a vector<string>.
std::vector<std::string> readStringList(Value* v) {
    std::vector<std::string> out;
    if (v == nullptr) {
        return out;
    }
    auto n = NestedVal::getChildrenSize(v);
    out.reserve(n);
    for (uint32_t i = 0; i < n; i++) {
        out.push_back(NestedVal::getChildVal(v, i)->toString());
    }
    return out;
}

// Run the anchored, bounded reified-path query and return up to `limit` shortest
// logical paths from `source` to `target` (target optional/empty = any endpoint).
std::vector<LogicalPath> findPaths(ClientContext* context, const std::string& source,
    const std::string& target, int64_t maxHops, int64_t limit) {
    if (maxHops < 1) {
        maxHops = 1;
    }
    const int64_t physMax = 2 * maxHops; // physical depth = 2 * logical hops (§1.2)

    std::ostringstream q;
    // Anchored on the exact entity name. On the dense reified graph, enumerating ALL
    // bounded paths blows up (millions of paths at physMax>=6), so when a target is
    // given we use the engine's SHORTEST path mode (bounded, no full enumeration).
    // SHORTEST requires a lower bound of 1, which is fine: an Entity->Entity reified
    // path is always even-length, so the shortest is the fewest logical hops.
    q << "MATCH (a:Entity {name: '" << cypherEscape(source) << "'})";
    if (!target.empty()) {
        q << ", (b:Entity {name: '" << cypherEscape(target) << "'})"
          << " MATCH p = (a)-[:RELATES_TO* SHORTEST 1.." << physMax << "]-(b)";
    } else {
        // No fixed target: bounded (small physMax) any-endpoint derivation.
        q << " MATCH p = (a)-[:RELATES_TO* 2.." << physMax << "]-(b:Entity)";
    }
    // drop molecular bloat (GENE) mid-path (§6 label filtering). Entity nodes carry a
    // labels[] column; predicate (RelatesToNode_) nodes do not, so guard on label(n).
    q << " WHERE all(n IN nodes(p) WHERE label(n) <> 'Entity'"
      << " OR NOT 'GENE' IN coalesce(n.labels, []))"
      // Use label(n) to separate predicate nodes (RelatesToNode_) from Entity nodes;
      // an Entity may legitimately have empty labels[], so labels-size is not a safe
      // discriminator. list_filter / list_transform are this engine's comprehension.
      << " RETURN list_transform(list_filter(nodes(p),"
      << " n -> label(n) = 'RelatesToNode_'), n -> n.name) AS preds,"
      << " list_transform(list_filter(nodes(p),"
      << " n -> label(n) = 'RelatesToNode_'), n -> n.uuid) AS edge_ids,"
      << " list_transform(list_filter(nodes(p),"
      << " n -> label(n) = 'Entity'), n -> n.name) AS ents,"
      << " length(p) / 2 AS hops"
      << " ORDER BY hops ASC LIMIT " << limit;

    std::vector<LogicalPath> paths;
    // Run the traversal on a SEPARATE connection to the same Database. The outer
    // CALL already holds this ClientContext's lock, so a nested context->query()
    // would deadlock on the context mutex; a fresh Connection has its own context
    // and reads the same (MVCC) snapshot.
    Connection conn(context->getDatabase());
    auto result = conn.query(q.str());
    if (result == nullptr || !result->isSuccess()) {
        return paths;
    }
    while (result->hasNext()) {
        auto tuple = result->getNext();
        LogicalPath lp;
        lp.predicates = readStringList(tuple->getValue(0));
        lp.edgeIds = readStringList(tuple->getValue(1));
        lp.entityNames = readStringList(tuple->getValue(2));
        lp.hops = tuple->getValue(3)->getValue<int64_t>();
        // Node-acyclicity guard (§2.1): drop paths that revisit an Entity.
        std::vector<std::string> sorted = lp.entityNames;
        std::sort(sorted.begin(), sorted.end());
        if (std::adjacent_find(sorted.begin(), sorted.end()) != sorted.end()) {
            continue;
        }
        paths.push_back(std::move(lp));
    }
    return paths;
}

// Composed confidence (Context Monoid, §3.2): prod(edgePrior) * decay^(hops-1).
double composeConfidence(const LogicalPath& p) {
    double conf = 1.0;
    for (size_t i = 0; i < p.predicates.size(); i++) {
        conf *= kEdgePrior;
    }
    int64_t hops = p.hops < 1 ? 1 : p.hops;
    conf *= std::pow(kDecay, static_cast<double>(hops - 1));
    return conf;
}

// Build the JSON proof array (§3.1): one step per logical hop.
std::string buildProof(const LogicalPath& p) {
    std::ostringstream js;
    js << "[";
    for (size_t i = 0; i < p.predicates.size(); i++) {
        const std::string canon = normalizePredicate(p.predicates[i]);
        const std::string edgeId = i < p.edgeIds.size() ? p.edgeIds[i] : "";
        const std::string src = i < p.entityNames.size() ? p.entityNames[i] : "";
        const std::string dst = (i + 1) < p.entityNames.size() ? p.entityNames[i + 1] : "";
        if (i > 0) {
            js << ",";
        }
        js << "{\"edge_id\":\"" << jsonEscape(edgeId) << "\",\"rule\":\"composition\",\"predicate\":\""
           << jsonEscape(canon) << "\",\"source\":\"" << jsonEscape(src) << "\",\"target\":\""
           << jsonEscape(dst) << "\",\"confidence\":" << kEdgePrior << "}";
    }
    js << "]";
    return js.str();
}

} // namespace

std::vector<ReasonRow> runEntails(ClientContext* context, const std::string& subject,
    const std::string& /*predicate*/, const std::string& object, int64_t maxHops) {
    // v1: presence of an anchored, bounded reified path subject ⇝ object entails
    // the claim (§2.1/§2.4 composition; the predicate is recorded in the proof).
    // Disjointness contradiction (§2.5) needs an OntologyDisjoint table, absent in
    // this graph, so the contradiction guard is a no-op here.
    auto paths = findPaths(context, subject, object, maxHops, 8);
    ReasonRow r;
    if (paths.empty()) {
        r.verdict = "unsupported";
        r.confidence = 0.0;
        r.proof = "[]";
        return {r};
    }
    // Pick the highest-confidence proof (shortest paths rank first, but compose to
    // be sure since decay is monotone in hops).
    const LogicalPath* best = &paths[0];
    double bestConf = composeConfidence(paths[0]);
    for (size_t i = 1; i < paths.size(); i++) {
        double c = composeConfidence(paths[i]);
        if (c > bestConf) {
            bestConf = c;
            best = &paths[i];
        }
    }
    r.verdict = "entailed";
    r.confidence = bestConf;
    r.hops = best->hops;
    r.proof = buildProof(*best);
    return {r};
}

std::vector<ReasonRow> runDerive(ClientContext* context, const std::string& source,
    const std::string& target, int64_t maxHops, double minConf, int64_t limit) {
    auto paths = findPaths(context, source, target, maxHops, limit);
    std::vector<ReasonRow> rows;
    rows.reserve(paths.size());
    for (const auto& p : paths) {
        double conf = composeConfidence(p);
        if (conf < minConf) {
            continue;
        }
        ReasonRow r;
        r.target = p.entityNames.empty() ? "" : p.entityNames.back();
        r.confidence = conf;
        r.hops = p.hops;
        r.proof = buildProof(p);
        rows.push_back(std::move(r));
    }
    return rows;
}

std::vector<ReasonRow> runContradicts(ClientContext* /*context*/, const std::string& /*subject*/,
    const std::string& /*object*/) {
    // Disjointness/conflict detection (§2.5) requires an OntologyDisjoint side
    // table populated from SNOMED/MONDO; it is not present in this graph, so v1
    // reports no contradiction. Wiring is in place for when that table exists.
    ReasonRow r;
    r.contradicted = false;
    r.proof = "[]";
    return {r};
}

} // namespace reasoning_extension
} // namespace lbug
