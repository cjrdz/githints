const config = { "/not-a-route": true };

Bun.serve({
  port: 3000,
  routes: {
    "/": new Response("home"),
    "/api/status": () => Response.json({ ok: true }),
  },
});
