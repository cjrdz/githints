import { Elysia } from "elysia";

export const v1 = new Elysia({ prefix: "/v1" })
  .get("/users", () => [])
  .post("/users", () => ({}));

export const plain = new Elysia()
  .get("/ping", () => "pong");
