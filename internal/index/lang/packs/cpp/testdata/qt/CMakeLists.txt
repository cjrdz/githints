cmake_minimum_required(VERSION 3.21)
project(todo LANGUAGES CXX)

find_package(Qt6 REQUIRED COMPONENTS Widgets)

qt_add_executable(todo
  src/main.cpp
  src/ui/mainwindow.cpp
  src/models/todomodel.cpp)
target_link_libraries(todo PRIVATE Qt6::Widgets)
