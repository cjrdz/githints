#pragma once

#include "crow.h"

crow::response create_user(const crow::request &req);
crow::response delete_user(int id);
