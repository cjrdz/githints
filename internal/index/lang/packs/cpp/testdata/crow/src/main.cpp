#include "crow.h"
#include "users.h"

int main()
{
    crow::SimpleApp app;

    CROW_ROUTE(app, "/")([]() { return "ok"; });

    CROW_ROUTE(app, "/users").methods("POST"_method)
    ([](const crow::request &req) { return create_user(req); });

    CROW_ROUTE(app, "/users/<int>")
        .methods(crow::HTTPMethod::Delete)([](int id) { return delete_user(id); });

    crow::Blueprint api("api");
    CROW_BP_ROUTE(api, "/health")([]() { return "up"; });

    CROW_WEBSOCKET_ROUTE(app, "/ws")
        .onmessage([](crow::websocket::connection &conn, const std::string &data, bool) {});

    CROW_CATCHALL_ROUTE(app)([]() { return "not found"; });

    // CROW_ROUTE(app, "/commented")
    const char *doc = "CROW_ROUTE(app, \"/in-a-string\")";

    app.port(18080).multithreaded().run();
}
