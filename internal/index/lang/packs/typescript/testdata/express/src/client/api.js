import axios from "axios";

// An HTTP client has the shape of a route; this file does not import express.
const api = axios.create({ baseURL: "/" });
export const listUsers = () => api.get("/users");
