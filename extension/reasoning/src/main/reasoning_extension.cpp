#include "main/reasoning_extension.h"

#include "function/reasoning_function.h"
#include "main/client_context.h"

namespace lbug {
namespace reasoning_extension {

using namespace extension;

void ReasoningExtension::load(main::ClientContext* context) {
    auto& db = *context->getDatabase();
    ExtensionUtils::addTableFunc<EntailsFunction>(db);
    ExtensionUtils::addTableFunc<DeriveFunction>(db);
    ExtensionUtils::addTableFunc<ContradictsFunction>(db);
}

} // namespace reasoning_extension
} // namespace lbug

#if defined(BUILD_DYNAMIC_LOAD)
extern "C" {
#if defined(_WIN32)
#define INIT_EXPORT __declspec(dllexport)
#else
#define INIT_EXPORT __attribute__((visibility("default")))
#endif
INIT_EXPORT void init(lbug::main::ClientContext* context) {
    lbug::reasoning_extension::ReasoningExtension::load(context);
}

INIT_EXPORT const char* name() {
    return lbug::reasoning_extension::ReasoningExtension::EXTENSION_NAME;
}
}
#endif
