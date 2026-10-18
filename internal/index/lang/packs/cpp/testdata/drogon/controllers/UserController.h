#pragma once

#include <drogon/HttpController.h>

using namespace drogon;

namespace api {

class UserController : public drogon::HttpController<UserController>
{
public:
    METHOD_LIST_BEGIN
    ADD_METHOD_TO(UserController::list, "/api/users", Get);
    ADD_METHOD_TO(UserController::create, "/api/users", Post, "AuthFilter");
    METHOD_ADD(UserController::get, "/{id}", Get);
    METHOD_LIST_END

    void list(const HttpRequestPtr &req, std::function<void(const HttpResponsePtr &)> &&cb);
    void create(const HttpRequestPtr &req, std::function<void(const HttpResponsePtr &)> &&cb);
    void get(const HttpRequestPtr &req, std::function<void(const HttpResponsePtr &)> &&cb, int id);
};

class HealthController : public drogon::HttpSimpleController<HealthController>
{
public:
    PATH_LIST_BEGIN
    PATH_ADD("/health", Get, Head);
    PATH_LIST_END
};

class ChatSocket : public drogon::WebSocketController<ChatSocket>
{
public:
    WS_PATH_LIST_BEGIN
    WS_PATH_ADD("/chat");
    // WS_PATH_ADD("/old");
    WS_PATH_LIST_END
};

}  // namespace api
