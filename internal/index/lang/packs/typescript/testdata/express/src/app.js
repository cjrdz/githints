const express = require("express");
const cors = require("cors");
const usersRouter = require("./routes/users");

const app = express();
app.use(cors());
app.use(express.json());
// Mounted from another file: its routes keep their own paths.
app.use("/api/users", usersRouter);

// A settings getter, not a route.
const mode = app.get("env");

app.get("/health", (req, res) => res.send("ok"));

module.exports = app;
