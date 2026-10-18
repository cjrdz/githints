import Foundation
import SwiftData

@Model
final class Entry {
    var text: String
    init(text: String) { self.text = text }
}
