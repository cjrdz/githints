const { z } = require("zod");

const UserSchema = z.object({
  name: z.string(),
});

module.exports = { UserSchema };
