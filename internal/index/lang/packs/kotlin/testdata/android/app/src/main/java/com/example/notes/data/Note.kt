package com.example.notes.data

import androidx.room.Dao
import androidx.room.Entity
import androidx.room.PrimaryKey
import androidx.room.Query

@Entity(tableName = "notes")
data class Note(
    @PrimaryKey val id: Long,
    val text: String,
)

@Dao
interface NoteDao {
    @Query("SELECT * FROM notes")
    suspend fun all(): List<Note>
}
