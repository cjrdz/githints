import { Hono } from "hono";
import { logger } from "hono/logger";
import { users } from "./db/schema";

const app = new Hono().basePath("/api");
app.use("*", logger());
app.get("/health", (c) => c.text("ok"));

const books = new Hono();
books.get("/:id", (c) => c.json({ id: c.req.param("id") }));
books.post("/", (c) => c.json({}, 201));
app.route("/books", books);

export default app;
