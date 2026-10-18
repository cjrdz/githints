mod handlers;
mod models;
mod schema;

use axum::{routing::{get, post}, Router};
use crate::handlers::todos;

#[tokio::main]
async fn main() {
    let app = Router::new()
        .route("/", get(todos::index))
        .route("/todos", get(todos::list).post(todos::create))
        .route("/todos/:id", axum::routing::delete(todos::remove));
    // .route("/commented-out", get(nope))
    let _ = app;
}
