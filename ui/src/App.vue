<template>
  <div id="app" class="sondns-app">
    <header class="sondns-titlebar">
      <div class="sondns-drag-region">
        <div class="brand-mark" aria-hidden="true">S</div>
        <span class="brand-name">SonDNS</span>
      </div>
      <div class="titlebar-status" :class="statusClass">
        <span class="status-dot" aria-hidden="true"></span>{{ statusLabel }}
      </div>
      <div class="window-actions">
        <button class="window-button" title="Minimize" @click="windowAction('minimize')">&minus;</button>
        <button class="window-button close" title="Close" @click="windowAction('close')">&times;</button>
      </div>
    </header>

    <main class="sondns-body">
      <div v-if="route.path !== '/'" class="sondns-subpage-header">
        <RouterLink to="/" class="subpage-back" aria-label="Back to home">‹</RouterLink>
        <div>
          <div class="section-kicker">SONDNS</div>
          <h1>{{ currentLabel }}</h1>
        </div>
        <div class="subpage-meta">
          <span>v{{ versionLabel }}</span>
          <span class="status-dot" :class="statusClass" aria-hidden="true"></span>{{ statusLabel }}
        </div>
      </div>

      <div v-if="store.state.error" class="error-banner">{{ store.state.error }}</div>
      <div class="sondns-route-viewport" :class="{ 'home-viewport': route.path === '/' }">
        <RouterView />
      </div>
    </main>
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
const labels = { "/": "Home", "/dns": "DNS", "/dpi": "DPI mode", "/logs": "Logs", "/settings": "Settings", "/diagnostics": "Diagnostics" };
const currentLabel = computed(() => labels[route.path] || "Home");
const statusLabel = computed(() => {
  if (store.state.error) return "Error";
  if (store.state.loading) return "Connecting";
  if (["start_pending", "stop_pending"].includes(store.state.service.state)) return "Connecting";
  if (store.state.service.state === "running") {
    if (!store.getters.isRunning || store.state.coreState?.asset_verified === false) return "Degraded";
    return "Protected";
  }
  return "Unprotected";
});
const statusClass = computed(() => statusLabel.value.toLowerCase());
const versionLabel = computed(() => store.state.diagnostics?.version || "dev");

onMounted(() => store.dispatch("refresh"));
watch(() => route.path, (path) => store.dispatch("saveUI", { lastRoute: path }));
watch(() => store.state.ui.lastRoute, (path) => {
  if (path && path !== route.path && route.path === "/") router.replace(path).catch(() => {});
}, { once: true });
</script>

<style lang="scss">
@import "@/components/scss/constants";

:root {
  --sondns-accent: #398fe6;
  --sondns-accent-hover: #2f7dd0;
  --sondns-stage: #e9eff1;
  --sondns-stage-line: rgba(77, 125, 158, 0.2);
  --sondns-panel-width: 310px;
}

* { box-sizing: border-box; }
html, body, #app { width: 100%; height: 100%; margin: 0; overflow: hidden; }
body { background: var(--background-color); }
button, select, input { font: inherit; }
button { color: inherit; }

.sondns-app {
  height: 100%;
  color: var(--text-color);
  background: var(--background-color);
  font-family: $base-font-family;
  font-size: 13px;
  letter-spacing: -0.2px;
}

.sondns-titlebar {
  height: 38px;
  display: flex;
  align-items: center;
  border-bottom: 1px solid var(--separator-line-color);
  -webkit-app-region: drag;
}

.sondns-drag-region { height: 100%; display: flex; align-items: center; gap: 8px; padding: 0 14px; }
.brand-mark { width: 19px; height: 19px; display: grid; place-items: center; border-radius: 5px; background: var(--sondns-accent); color: #fff; font-size: 13px; font-weight: 700; }
.brand-name { font-weight: 600; letter-spacing: -0.2px; }
.titlebar-status { display: flex; align-items: center; gap: 6px; margin-left: auto; color: var(--text-color-details); font-size: 11px; }
.status-dot { width: 7px; height: 7px; display: inline-block; border-radius: 50%; background: #98a4b2; }
.status-dot.protected { background: #36b37e; }.status-dot.degraded { background: #e7a83b; }.status-dot.error { background: #ff6258; }
.titlebar-status.protected { color: #319968; }.titlebar-status.degraded { color: #b47c1b; }.titlebar-status.error { color: #d84d45; }
.window-actions { height: 100%; display: flex; margin-left: 14px; -webkit-app-region: no-drag; }
.window-button { width: 42px; height: 100%; border: 0; background: transparent; color: var(--text-color-details); font-size: 17px; cursor: pointer; }
.window-button:hover { background: var(--background-color-alternate); color: var(--text-color); }.window-button.close:hover { background: #e81123; color: #fff; }

.sondns-body { height: calc(100% - 38px); display: flex; flex-direction: column; min-height: 0; }
.sondns-route-viewport { min-height: 0; flex: 1; overflow: auto; }.sondns-route-viewport.home-viewport { overflow: hidden; }
.sondns-route-viewport::-webkit-scrollbar { width: 7px; height: 7px; }.sondns-route-viewport::-webkit-scrollbar-thumb { background: rgba(139, 154, 171, 0.4); border-radius: 5px; }
.sondns-subpage-header { min-height: 72px; display: flex; align-items: center; gap: 14px; padding: 12px 22px; border-bottom: 1px solid var(--separator-line-color); background: var(--background-color); }
.subpage-back { width: 26px; height: 26px; display: grid; place-items: center; color: var(--text-color-details); text-decoration: none; border-radius: 50%; font-size: 27px; line-height: 1; }.subpage-back:hover { color: var(--text-color); background: var(--background-color-alternate); }
.section-kicker { color: var(--text-color-details); font-size: 10px; letter-spacing: 1.2px; text-transform: uppercase; }.sondns-subpage-header h1 { margin: 2px 0 0; font-size: 21px; font-weight: 500; letter-spacing: -0.6px; }
.subpage-meta { display: flex; align-items: center; gap: 7px; margin-left: auto; color: var(--text-color-details); font-size: 11px; }
.error-banner { margin: 10px 20px 0; padding: 9px 12px; border: 1px solid rgba(255, 98, 88, 0.28); border-radius: 5px; background: rgba(255, 98, 88, 0.1); color: #c54f49; font-size: 12px; }

@media (max-width: 650px) { :root { --sondns-panel-width: 280px; } .titlebar-status { display: none; } .sondns-subpage-header { padding-right: 14px; padding-left: 14px; } }
</style>
