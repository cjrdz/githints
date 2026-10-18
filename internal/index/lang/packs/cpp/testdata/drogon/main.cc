#include <drogon/drogon.h>

int main()
{
    drogon::app().registerHandler("/ping",
        [](const drogon::HttpRequestPtr &req,
           std::function<void(const drogon::HttpResponsePtr &)> &&callback) {
            callback(drogon::HttpResponse::newHttpResponse());
        },
        {drogon::Get});
    drogon::app().addListener("0.0.0.0", 8080).run();
}
