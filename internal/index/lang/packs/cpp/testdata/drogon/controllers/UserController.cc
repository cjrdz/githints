#include "UserController.h"

void api::UserController::list(const HttpRequestPtr &req, std::function<void(const HttpResponsePtr &)> &&cb)
{
    cb(HttpResponse::newHttpResponse());
}
