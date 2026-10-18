import SwiftUI
import JournalKit

struct EntryList: View {
    var body: some View { Text("Entries") }
}

struct EntryRow: Identifiable, View {
    let id = UUID()
    var body: some View { Text("\(id)") }
}

// Not a view: a plain struct.
struct Formatter {
    static func format() -> String { "" }
}
