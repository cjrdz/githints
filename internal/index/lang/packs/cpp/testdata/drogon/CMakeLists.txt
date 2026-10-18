cmake_minimum_required(VERSION 3.5)
project(shop CXX)
find_package(Drogon CONFIG REQUIRED)
add_executable(shop main.cc controllers/UserController.cc)
