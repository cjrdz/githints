import { initTRPC } from "@trpc/server";
import { z } from "zod";

const t = initTRPC.create();
const publicProcedure = t.procedure;

export const appRouter = t.router({
  getUser: publicProcedure
    .input(z.object({ id: z.string() }))
    .query(({ input }) => ({ id: input.id })),
  createUser: publicProcedure
    .input(z.object({ name: z.string() }))
    .mutation(({ input }) => input),
  onTick: t.procedure.subscription(() => null),
  version: "1.0",
});
