import express, { Router } from "express";

const api = Router();
const admin = Router();

admin.get("/stats", (req, res) => res.json({}));
api.use("/admin", admin);

export default api;
