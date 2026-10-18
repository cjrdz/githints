package com.example

import com.example.plugins.configureRouting
import io.ktor.server.application.*

fun Application.module() {
    configureRouting()
}
