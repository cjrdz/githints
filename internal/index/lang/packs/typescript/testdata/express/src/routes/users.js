const { Router } = require("express");
const User = require("../models/User");
const { UserSchema } = require("../schemas/user");

const router = Router();

router.get("/", async (req, res) => res.json(await User.find()));

router.post(
  "/:id",
  async (req, res) => res.json(UserSchema.parse(req.body)),
);

router.route("/:id/avatar").get((req, res) => res.end());

module.exports = router;
