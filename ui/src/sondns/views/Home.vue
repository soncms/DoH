<template>
  <section>
    <div class="sondns-card">
      <div class="sondns-status-large">
        <div class="state">{{ status }}</div>
        <div class="sub">{{ serviceLine }}</div>
        <div class="sondns-actions">
          <button v-if="!running" class="sondns-button" @click="start">{{ !installed ? "Install & start" : needsMigration ? "Migrate & start" : "Start" }}</button>
          <button v-else class="sondns-button secondary" @click="stop">Stop</button>
          <button class="sondns-button secondary" :disabled="busy" @click="refresh">Refresh</button>
        </div>
      </div>
    </div>
    <div class="sondns-kpi-grid">
      <div class="sondns-kpi"><div class="sondns-kpi-label">DNS provider</div><div class="sondns-kpi-value">{{ provider }}</div></div>
      <div class="sondns-kpi"><div class="sondns-kpi-label">DPI mode</div><div class="sondns-kpi-value">{{ dpiLabel }}</div></div>
      <div class="sondns-kpi"><div class="sondns-kpi-label">WinDivert</div><div class="sondns-kpi-value">{{ windivert }}</div></div>
      <div class="sondns-kpi"><div class="sondns-kpi-label">Uptime</div><div class="sondns-kpi-value">{{ uptime }}</div></div>
    </div>
    <div class="sondns-card" style="margin-top:12px">
      <div class="sondns-card-title">Runtime health <span class="sondns-muted">{{ assetSummary }}</span></div>
      <div class="sondns-card-subtitle">DNS interception remains available when DPI is disabled. DPI helper failures fail open and are recorded in diagnostics.</div>
      <div class="sondns-row" style="margin-top:8px"><span class="sondns-label">Requests in memory</span><span class="sondns-muted">{{ store.state.entries.length }} / 200</span></div>
      <div class="sondns-row"><span class="sondns-label">Last action</span><span class="sondns-muted">{{ store.state.lastAction || "Ready" }}</span></div>
    </div>
  </section>
</template>
<script setup>
import { computed } from "vue";
import { useStore } from "vuex";
const store = useStore();
const busy = computed(() => store.state.loading);
const installed = computed(() => store.state.service.exists);
const needsMigration = computed(() => installed.value && !/SonDNS\.Service\.exe/i.test(store.state.service.binary_path || ""));
const running = computed(() => store.getters.isRunning);
const status = computed(() => {
  if (store.state.error) return "Error";
  if (store.state.loading || ["start_pending", "stop_pending"].includes(store.state.service.state)) return "Connecting";
  if (!installed.value) return "Unprotected";
  if (!running.value || store.state.coreState?.asset_verified === false) return "Degraded";
  return "Protected";
});
const provider = computed(() => store.getters.provider);
const dpiLabel = computed(() => store.getters.dpiLabel);
const windivert = computed(() => store.state.coreState?.dns_running ? "Active" : "Standby");
const uptime = computed(() => { const seconds = store.state.coreState?.uptime_seconds || 0; if (!seconds) return "—"; const h = Math.floor(seconds / 3600); const m = Math.floor((seconds % 3600) / 60); return h ? `${h}h ${m}m` : `${m}m`; });
const serviceLine = computed(() => { if (!installed.value) return "Service is not installed"; return store.state.service.auto_start ? "Service running automatically" : "Service starts manually"; });
const assetSummary = computed(() => { const assets = store.state.diagnostics?.assets || []; if (!assets.length) return "not checked"; const good = assets.filter((asset) => asset.verified).length; return `${good}/${assets.length} assets verified`; });
const refresh = () => store.dispatch("refresh");
const start = () => store.dispatch("serviceAction", installed.value && !needsMigration.value ? "start" : "install");
const stop = () => store.dispatch("serviceAction", "stop");
</script>
