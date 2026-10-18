#[tauri::command]
pub fn greet(name: &str) -> String {
    format!("Hello, {}!", name)
}

#[tauri::command(rename_all = "snake_case")]
pub async fn save_note(text: String) -> Result<(), String> {
    Ok(())
}

pub fn not_a_command() {}
