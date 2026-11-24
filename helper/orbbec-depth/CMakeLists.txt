cmake_minimum_required(VERSION 3.15)
project(orbbec-depth LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 11)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

set(ORBBEC_SDK_DIR "" CACHE PATH "Orbbec SDK directory (include/, lib/, config/)")
if(NOT ORBBEC_SDK_DIR)
  message(FATAL_ERROR "ORBBEC_SDK_DIR is required")
endif()

add_executable(orbbec-depth main.cpp)
target_include_directories(orbbec-depth PRIVATE
  ${ORBBEC_SDK_DIR}/include
  ${ORBBEC_SDK_DIR}/include/libobsensor/h
)
target_link_directories(orbbec-depth PRIVATE ${ORBBEC_SDK_DIR}/lib)

if(WIN32)
  target_link_libraries(orbbec-depth PRIVATE OrbbecSDK ws2_32)
elseif(APPLE)
  target_link_libraries(orbbec-depth PRIVATE OrbbecSDK)
  set_target_properties(orbbec-depth PROPERTIES BUILD_RPATH "@executable_path" INSTALL_RPATH "@executable_path")
else()
  target_link_libraries(orbbec-depth PRIVATE OrbbecSDK)
  set_target_properties(orbbec-depth PROPERTIES BUILD_RPATH "$ORIGIN" INSTALL_RPATH "$ORIGIN")
endif()
