import { Routes } from "@angular/router";
import { UserCard } from "./user-card/user-card";

export const routes: Routes = [
  { path: "", redirectTo: "users", pathMatch: "full" },
  {
    path: "users/:id",
    component: UserCard,
  },
  { path: "**", redirectTo: "" },
];
