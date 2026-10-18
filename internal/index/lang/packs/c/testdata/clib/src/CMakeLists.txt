add_library(clib STATIC util.c)
target_include_directories(clib PUBLIC ${PROJECT_SOURCE_DIR}/include)
add_executable(clib-cli main.c)
target_link_libraries(clib-cli PRIVATE clib)
