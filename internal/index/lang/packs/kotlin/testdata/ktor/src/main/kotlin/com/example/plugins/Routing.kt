package com.example.plugins

import io.ktor.server.application.*
import io.ktor.server.response.*
import io.ktor.server.routing.*

fun Application.configureRouting() {
    routing {
        get("/") {
            call.respondText("Hello")
        }
        route("/api") {
            route("/users") {
                get {
                    val id = call.parameters.get("id")
                    call.respondText("list")
                }
                post("/{id}") { }
            }
            delete("/cache") { }
        }
        // get("/commented-out") { }
    }
}
