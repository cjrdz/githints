#pragma once

#include "oatpp/web/server/api/ApiController.hpp"
#include "oatpp/core/macro/codegen.hpp"

#include OATPP_CODEGEN_BEGIN(ApiController)

class UserController : public oatpp::web::server::api::ApiController {
public:
  ENDPOINT("GET", "/users/{id}", getUser, PATH(Int32, id)) {
    return createResponse(Status::CODE_200, "user");
  }

  ENDPOINT("POST", "/users", createUser, BODY_STRING(String, body)) {
    return createResponse(Status::CODE_201, body);
  }
};

#include OATPP_CODEGEN_END(ApiController)
