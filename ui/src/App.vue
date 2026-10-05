<template>
  <div id="app" class="sondns-app">
    <div class="sondns-titlebar">
      <div class="sondns-drag-region">
        <div class="brand-mark">S</div>
        <span class="brand-name">SonDNS</span>
      </div>
      <div class="window-actions">
        <button class="window-button" title="Minimize" @click="windowAction('minimize')">&minus;</button>
        <button class="window-button close" title="Close" @click="windowAction('close')">&times;</button>
      </div>
    </div>

    <div class="sondns-layout">
      <aside class="sondns-sidebar">
        <nav>
          <RouterLink v-for="item in nav" :key="item.path" :to="item.path" class="nav-item">
            <span class="nav-icon">{{ item.icon }}</span><span>{{ item.label }}</span>
          </RouterLink>
        </nav>
        <div class="sidebar-bottom">
          <span class="version-label">{{ versionLabel }}</span>
          <RouterLink to="/diagnostics" class="nav-item compact"><span class="nav-icon">ⓘ</span><span>Diagnostics</span></RouterLink>
        </div>
      </aside>

      <main class="sondns-main">
        <header class="page-header">
          <div>
            <div class="eyebrow">SONDNS CLIENT</div>
            <h1>{{ currentLabel }}</h1>
          </div>
          <div class="header-status" :class="statusClass"><span class="status-dot"></span>{{ statusLabel }}</div>
        </header>
        <div v-if="$store.state.error" class="error-banner">{{ $store.state.error }}</div>
        <RouterView />
      </main>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useStore } from "vuex";
import { windowAction } from "./sondns/api";

const store = useStore();
const route = useRoute();
const router = useRouter();
const nav = [
  { path: "/", label: "Home", icon: "◉" },
  { path: "/dns", label: "DNS", icon: "⌁" },
  { path: "/dpi", label: "DPI mode", icon: "◌" },
  { path: "/logs", label: "Logs", icon: "≡" },
  { path: "/settings", label: "Settings", icon: "⚙" },
];
const labels = { "/": "Home", "/dns": "DNS", "/dpi": "DPI mode", "/logs": "Logs", "/settings": "Settings", "/diagnostics": "Diagnostics" };
const currentLabel = computed(() => labels[route.path] || "Home");
const statusLabel = computed(() => {
	if (store.state.error) return "Error";
  if (store.state.loading) return "Loading";
  if (store.state.service.state === "start_pending" || store.state.service.state === "stop_pending") return "Connecting";
  if (store.state.service.state === "running") {
    if (!store.getters.isRunning || store.state.coreState?.asset_verified === false) return "Degraded";
    return "Protected";
  }
  return "Unprotected";
});
const statusClass = computed(() => statusLabel.value.toLowerCase());
const versionLabel = computed(() => store.state.diagnostics?.version ? `v${store.state.diagnostics.version}` : "vdev");

onMounted(() => store.dispatch("refresh"));
watch(() => route.path, (path) => store.dispatch("saveUI", { lastRoute: path }));
watch(() => store.state.ui.lastRoute, (path) => {
  if (path && path !== route.path && route.path === "/") router.replace(path).catch(() => {});
}, { once: true });
</script>

<style lang="scss">
@import "@/components/scss/constants";

:root { --sondns-accent: #398fe6; --sondns-accent-strong: #2f7dd0; --sondns-card: rgba(128, 145, 165, 0.12); --sondns-card-border: rgba(139, 154, 171, 0.25); }
* { box-sizing: border-box; }
html, body, #app { margin: 0; width: 100%; height: 100%; overflow: hidden; }
body { background: var(--background-color); }
.sondns-app { height: 100%; background: var(--background-color); color: var(--text-color); font-family: $base-font-family; font-size: 13px; }
.sondns-titlebar { height: 38px; display:flex; align-items:center; justify-content:space-between; border-bottom:1px solid var(--separator-line-color); -webkit-app-region: drag; }
.sondns-drag-region { display:flex; align-items:center; gap:8px; height:100%; padding-left:14px; }
.brand-mark { width:19px; height:19px; display:grid; place-items:center; color:#fff; font-size:13px; font-weight:700; border-radius:5px; background:var(--sondns-accent); }
.brand-name { font-weight:600; letter-spacing:-.2px; }
.window-actions { display:flex; height:100%; -webkit-app-region:no-drag; }
.window-button { width:42px; border:0; background:transparent; color:var(--text-color-details); font-size:17px; cursor:pointer; }
.window-button:hover { background:var(--background-color-alternate); color:var(--text-color); }
.window-button.close:hover { background:#e81123; color:#fff; }
.sondns-layout { height:calc(100% - 38px); display:flex; }
.sondns-sidebar { width:126px; display:flex; flex-direction:column; justify-content:space-between; padding:18px 10px 12px; border-right:1px solid var(--separator-line-color); background:var(--background-color-alternate); }
.nav-item { display:flex; align-items:center; gap:9px; min-height:34px; padding:0 9px; margin:3px 0; border-radius:6px; color:var(--text-color-details); text-decoration:none; font-size:12px; letter-spacing:-.1px; }
.nav-item:hover { color:var(--text-color); background:var(--sondns-card); }
.nav-item.router-link-active { color:var(--text-color); background:var(--sondns-card); font-weight:600; }
.nav-item.compact { font-size:11px; }
.nav-icon { width:17px; text-align:center; font-size:15px; opacity:.85; }
.sidebar-bottom { color:var(--text-color-details); }
.version-label { display:block; padding:10px 9px 3px; font-size:10px; }
.sondns-main { min-width:0; flex:1; overflow:auto; padding:20px 20px 25px; }
.sondns-main::-webkit-scrollbar { width:6px; }
.sondns-main::-webkit-scrollbar-thumb { background:var(--sondns-card-border); border-radius:4px; }
.page-header { display:flex; align-items:flex-start; justify-content:space-between; margin-bottom:18px; }
.eyebrow { color:var(--text-color-details); font-size:10px; letter-spacing:1px; margin-bottom:4px; }
h1 { margin:0; font-size:23px; font-weight:500; letter-spacing:-.6px; }
.header-status { display:flex; align-items:center; gap:6px; font-size:11px; padding-top:5px; color:var(--text-color-details); }
.status-dot { width:7px; height:7px; border-radius:50%; background:#98a4b2; }
.header-status.protected { color:#36b37e; }.header-status.protected .status-dot { background:#36b37e; }
.header-status.degraded { color:#e7a83b; }.header-status.degraded .status-dot { background:#e7a83b; }
.header-status.error { color:#ff6258; }.header-status.error .status-dot { background:#ff6258; }
.error-banner { padding:10px 12px; margin:-4px 0 14px; border-radius:6px; background:rgba(255,98,88,.13); color:#ff8178; font-size:12px; }
button, select, input { font:inherit; }
button { color:inherit; }
</style>
