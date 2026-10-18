import { Worker } from "bullmq";

export const emails = new Worker("emails", async (job) => {
  return job.data;
});
