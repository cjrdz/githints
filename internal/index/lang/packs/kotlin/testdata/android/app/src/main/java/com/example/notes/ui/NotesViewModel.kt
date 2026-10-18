package com.example.notes.ui

import androidx.lifecycle.ViewModel
import com.example.notes.data.NoteDao

class NotesViewModel(private val dao: NoteDao) : ViewModel() {
    fun load() {}
}
