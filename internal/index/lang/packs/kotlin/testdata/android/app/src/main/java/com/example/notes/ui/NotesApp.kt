package com.example.notes.ui

import androidx.compose.runtime.Composable
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.rememberNavController
import com.example.notes.data.NoteDao

@Composable
fun NotesApp() {
    val nav = rememberNavController()
    NavHost(nav, startDestination = "notes") {
        composable("notes") { NoteList() }
        composable(route = "notes/{id}") { NoteDetail() }
    }
}

@Composable
private fun NoteList() {}

@Composable
@Suppress("UNUSED")
fun NoteDetail(
    id: String = "",
) {}

// A lowercase composable helper is still a component by Compose's own rules,
// but the convention reserves capitals for UI; helpers stay out.
@Composable
fun rememberSomething(): Int = 0
