#include <httplib.h>
#include <string>

static void handle_stats(const httplib::Request &, httplib::Response &res);

int main()
{
    httplib::Server svr;

    svr.Get("/hi", [](const httplib::Request &, httplib::Response &res) {
        res.set_content("Hello", "text/plain");
    });
    svr.Post("/items", &handle_stats);
    svr.Get(R"(/numbers/(\d+))", [&](const httplib::Request &req, httplib::Response &res) {
        res.set_content(req.matches[1], "text/plain");
    });

    // The client has the same methods; these are requests, not routes.
    httplib::Client cli("localhost", 8080);
    auto r = cli.Get("/hi");
    auto p = cli.Post("/items", "{}", "application/json");

    svr.listen("0.0.0.0", 8080);
}
