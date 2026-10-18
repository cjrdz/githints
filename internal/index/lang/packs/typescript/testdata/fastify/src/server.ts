import Fastify from "fastify";
import users from "./plugins/users";

const fastify = Fastify();

fastify.addHook("onRequest", async () => {});
fastify.get("/health", async () => ({ ok: true }));
fastify.route({
  method: "POST",
  url: "/items",
  handler: async () => ({}),
});
fastify.route({ url: "/items/:id", method: "DELETE", handler: async () => null });
fastify.register(users, { prefix: "/v1" });
