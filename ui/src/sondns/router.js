import { createRouter, createWebHashHistory } from "vue-router";
import Home from "./views/Home.vue";
import DNS from "./views/DNS.vue";
import DPI from "./views/DPI.vue";
import Logs from "./views/Logs.vue";
import Settings from "./views/Settings.vue";
import Diagnostics from "./views/Diagnostics.vue";

const routes = [
  { path: "/", name: "home", component: Home },
  { path: "/dns", name: "dns", component: DNS },
  { path: "/dpi", name: "dpi", component: DPI },
  { path: "/logs", name: "logs", component: Logs },
  { path: "/settings", name: "settings", component: Settings },
  { path: "/diagnostics", name: "diagnostics", component: Diagnostics },
];

export default createRouter({ history: createWebHashHistory(), routes });
