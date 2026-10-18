#include <pistache/endpoint.h>
#include <pistache/router.h>

using namespace Pistache;

class Api {
public:
    void setupRoutes(Rest::Router &router)
    {
        Rest::Routes::Get(router, "/users/:id", Rest::Routes::bind(&Api::getUser, this));
        Rest::Routes::Post(router, "/users", Rest::Routes::bind(&Api::createUser, this));
    }

    void getUser(const Rest::Request &req, Http::ResponseWriter res);
    void createUser(const Rest::Request &req, Http::ResponseWriter res);
};
