import Observation

@Observable
final class EntryStore {
    var entries: [String] = []
    func add(_ text: String) {}
}
