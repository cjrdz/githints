import Fluent
import Vapor

final class Todo: Model, Content {
    static let schema = "todos"
}
