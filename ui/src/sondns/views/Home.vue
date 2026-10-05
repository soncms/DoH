<template>
  <section class="ivpn-home">
    <aside class="ivpn-control-panel">
      <div class="ivpn-panel-scroll">
        <div class="ivpn-connect-block">
          <div class="ivpn-connect-head">
            <div>
              <div class="ivpn-small-text">Your connection is</div>
              <div class="ivpn-large-status" role="status">{{ displayStatus }}</div>
              <div class="ivpn-status-caption">{{ serviceLine }}</div>
            </div>
            <div class="ivpn-connect-actions">
              <button class="ivpn-round-button" title="Refresh state" :disabled="busy" @click="refresh">↻</button>
              <button class="ivpn-switch" :class="{ on: running, busy }" role="switch" :aria-checked="running" :title="running ? 'Stop service' : 'Start service'" :disabled="busy" @click="toggleConnection">
                <span></span>
              </button>
            </div>
          </div>

          <div class="ivpn-mode-tabs" aria-label="SonDNS sections">
            <RouterLink to="/dns" :class="{ active: route.path === '/dns' || route.path === '/' }">DNS</RouterLink>
            <RouterLink to="/dpi" :class="{ active: route.path === '/dpi' }">DPI MODE</RouterLink>
          </div>

          <RouterLink to="/dns" class="ivpn-provider-select">
            <div class="ivpn-small-text">Connect to</div>
            <div class="ivpn-provider-line">
              <span class="ivpn-provider-dot" :class="{ inactive: !running }"></span>
              <span class="ivpn-provider-name">{{ provider }}</span>
              <span class="ivpn-chevron" aria-hidden="true">›</span>
            </div>
          </RouterLink>
        </div>

        <div class="ivpn-horizontal-line"></div>
        <div class="ivpn-section-label">CONNECTION DETAILS</div>

        <nav class="ivpn-detail-list" aria-label="Connection details">
          <RouterLink to="/dns" class="ivpn-detail-row">
            <div><div class="ivpn-detail-label">DNS provider</div><div class="ivpn-detail-description">Encrypted upstream</div></div>
            <div class="ivpn-detail-value">{{ provider }} <span class="ivpn-chevron">›</span></div>
          </RouterLink>
          <RouterLink to="/dpi" class="ivpn-detail-row">
            <div><div class="ivpn-detail-label">DPI mode</div><div class="ivpn-detail-description">Optional traffic helper</div></div>
            <div class="ivpn-detail-value">{{ dpiLabel }} <span class="ivpn-chevron">›</span></div>
          </RouterLink>
          <div class="ivpn-detail-row">
            <div><div class="ivpn-detail-label">WinDivert</div><div class="ivpn-detail-description">DNS interception</div></div>
            <div class="ivpn-detail-value">{{ windivert }}</div>
          </div>
          <RouterLink to="/logs" class="ivpn-detail-row">
            <div><div class="ivpn-detail-label">DNS requests</div><div class="ivpn-detail-description">Recent activity</div></div>
            <div class="ivpn-detail-value">{{ store.state.entries.length }} <span class="ivpn-chevron">›</span></div>
          </RouterLink>
        </nav>

        <div class="ivpn-panel-links">
          <RouterLink to="/settings">Settings <span class="ivpn-chevron">›</span></RouterLink>
          <RouterLink to="/diagnostics">Diagnostics <span class="ivpn-chevron">›</span></RouterLink>
        </div>
      </div>
    </aside>

    <div class="ivpn-stage">
      <div class="ivpn-stage-actions">
        <RouterLink to="/settings" class="ivpn-stage-button" title="Settings">⚙</RouterLink>
        <RouterLink to="/diagnostics" class="ivpn-stage-button" title="Diagnostics">i</RouterLink>
      </div>
      <div class="ivpn-network-art" aria-hidden="true">
        <div class="ivpn-network-grid"></div>
        <div class="ivpn-orbit ivpn-orbit-one"></div>
        <div class="ivpn-orbit ivpn-orbit-two"></div>
        <div class="ivpn-network-node node-one"></div>
        <div class="ivpn-network-node node-two"></div>
        <div class="ivpn-network-node node-three"></div>
        <div class="ivpn-network-core"><span>S</span></div>
      </div>
      <div class="ivpn-stage-copy">
        <div class="ivpn-stage-kicker">SONDNS RESOLVER</div>
        <div class="ivpn-stage-title">{{ provider }} route</div>
        <div class="ivpn-stage-subtitle">{{ stageSubtitle }}</div>
      </div>
      <div class="ivpn-stage-footer">
        <span class="ivpn-stage-health" :class="{ good: running && assetHealthy }"></span>
        {{ assetSummary }}<span class="ivpn-stage-separator">·</span>{{ uptime }} uptime
      </div>
    </div>
  </section>
</template>

<script setup>
import { computed } from "vue";
import { useRoute } from "vue-router";
import { useStore } from "vuex";

const store = useStore();
const route = useRoute();
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
const displayStatus = computed(() => status.value.toLowerCase());
const provider = computed(() => store.getters.provider);
const dpiLabel = computed(() => store.getters.dpiLabel);
const windivert = computed(() => store.state.coreState?.dns_running ? "Active" : "Standby");
const uptime = computed(() => {
  const seconds = store.state.coreState?.uptime_seconds || 0;
  if (!seconds) return "—";
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  return h ? `${h}h ${m}m` : `${m}m`;
});
const serviceLine = computed(() => {
  if (!installed.value) return "Service is not installed";
  if (store.state.loading) return "Updating service state";
  return store.state.service.auto_start ? "Service starts automatically" : "Service starts manually";
});
const assets = computed(() => store.state.diagnostics?.assets || []);
const assetHealthy = computed(() => assets.value.length > 0 && assets.value.every((asset) => asset.verified));
const assetSummary = computed(() => {
  if (!assets.value.length) return "Assets not checked";
  const good = assets.value.filter((asset) => asset.verified).length;
  return `${good}/${assets.value.length} assets verified`;
});
const stageSubtitle = computed(() => {
  if (store.state.error) return "Action failed. Open diagnostics for details.";
  if (!installed.value) return "Install the service to begin interception.";
  if (!running.value) return "DNS interception is ready to start.";
  if (!assetHealthy.value && assets.value.length) return "Runtime asset verification needs attention.";
  return "Encrypted DNS interception is active.";
});

const refresh = () => store.dispatch("refresh");
const start = () => store.dispatch("serviceAction", installed.value && !needsMigration.value ? "start" : "install");
const stop = () => store.dispatch("serviceAction", "stop");
const toggleConnection = () => (running.value ? stop() : start());
</script>
