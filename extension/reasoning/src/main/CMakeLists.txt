add_library(reasoning_extension_main
        OBJECT
        reasoning_extension.cpp
        )

set(REASONING_EXTENSION_OBJECT_FILES
        ${REASONING_EXTENSION_OBJECT_FILES} $<TARGET_OBJECTS:reasoning_extension_main>
        PARENT_SCOPE)
