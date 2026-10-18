use actix_web::{get, post, web, App, HttpServer, Responder};

#[get("/")]
async fn index() -> impl Responder { "hi" }

#[post("/posts")]
async fn create_post() -> impl Responder { "ok" }

async fn list_users() -> impl Responder { "users" }

fn config(cfg: &mut web::ServiceConfig) {
    cfg.service(
        web::scope("/api")
            .route("/users", web::get().to(list_users))
            .service(web::resource("/health")),
    );
}
