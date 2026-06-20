add_library(lbug_reasoning_function
        OBJECT
        entails.cpp
        )

set(REASONING_EXTENSION_OBJECT_FILES
        ${REASONING_EXTENSION_OBJECT_FILES} $<TARGET_OBJECTS:lbug_reasoning_function>
        PARENT_SCOPE)
