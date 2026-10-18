import { type RouteConfig, index, prefix, route } from "@react-router/dev/routes";

export default [
  index("routes/home.tsx"),
  route("about", "routes/about.tsx"),
  ...prefix("projects", [
    index("routes/projects/list.tsx"),
    route(":pid", "routes/projects/show.tsx"),
  ]),
  route("settings", "routes/settings.tsx"),
] satisfies RouteConfig;
