cmake_minimum_required(VERSION 3.20)
project(clib C)

option(CLIB_TESTS "Build the tests" ON)

add_subdirectory(src)

#[[
add_subdirectory(legacy)
]]

if(CLIB_TESTS)
  enable_testing()
  add_executable(test_util tests/test_util.c)
  target_link_libraries(test_util PRIVATE clib)
  add_test(NAME util_test COMMAND test_util)
endif()

# A target a developer runs: cmake --build . --target format
add_custom_target(format
  COMMAND clang-format -i src/*.c include/clib/*.h)
