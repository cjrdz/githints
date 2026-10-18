import { createRouter, createWebHistory } from "vue-router";
import HomeView from "../views/HomeView.vue";

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: "/", name: "home", component: HomeView },
    {
      path: "/users/:id",
      component: () => import("../views/UserView.vue"),
    },
  ],
});
