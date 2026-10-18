// A route plugin: no import of fastify, only the instance it is given.
export default async function users(fastify: any) {
  fastify.get("/users", async () => []);
  fastify.put("/users/:id", async () => ({}));
}
