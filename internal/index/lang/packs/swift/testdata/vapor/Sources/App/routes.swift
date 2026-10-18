import Vapor

func routes(_ app: Application) throws {
    app.get { req async in "It works!" }
    app.get("hello", ":name") { req async -> String in "Hello" }

    let api = app.grouped("api")
    let todos = api.grouped("todos")
    todos.get(use: index)
    todos.post(use: create)
    todos.delete(":todoID", use: delete)
    // app.get("commented-out") { req in "" }
}
