import { createApp } from "vue";
import App from "./App.vue";
import router from "./router";
import store from "./store";
import "./main_style.js";
import "./sondns/styles.scss";

const app = createApp(App);
app.use(store);
app.use(router);
app.mount("#app");
window.sondns?.onNavigate((route) => router.push(route));


