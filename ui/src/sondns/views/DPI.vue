<template>
  <section>
    <div class="sondns-card">
      <div class="sondns-card-title">DPI mode <span class="sondns-card-subtitle">optional zapret2 helper</span></div>
      <div class="sondns-row"><div><div class="sondns-label">DPI mode</div><span class="sondns-help">Disabled keeps WinDivert DNS running without extracting or starting winws2.</span></div><select class="sondns-select" :value="value" @change="changeMode"><option value="disabled">Disabled</option><option value="default">Default</option><option value="tcp">TCP</option><option value="tcp+udp">TCP + QUIC UDP</option></select></div>
    </div>
    <div class="sondns-card">
      <div class="sondns-card-title">DPI runtime</div>
      <div class="sondns-row"><span class="sondns-label">Helper</span><span class="sondns-muted">{{ helperState }}</span></div>
      <div class="sondns-row"><span class="sondns-label">Capture scope</span><span class="sondns-muted">{{ captureMode }}</span></div>
      <div class="sondns-row"><span class="sondns-label">Failure policy</span><span class="sondns-muted">Fail open</span></div>
    </div>
    <div class="sondns-card">
      <div class="sondns-card-title">Asset verification</div>
      <div v-if="assets.length" class="sondns-table-wrap"><table class="sondns-table"><thead><tr><th>Asset</th><th>SHA-256</th><th>State</th></tr></thead><tbody><tr v-for="asset in assets" :key="asset.name"><td>{{ asset.name }}</td><td class="sondns-code">{{ shortHash(asset.actual || asset.expected) }}</td><td :class="asset.verified ? 'ok' : 'bad'">{{ asset.verified ? "Verified" : "Failed" }}</td></tr></tbody></table></div>
      <div v-else class="sondns-empty">Start the service to verify runtime assets.</div>
    </div>
  </section>
</template>
<script setup>
import { computed } from "vue";
import { useStore } from "vuex";
const store = useStore();
const value = computed(() => store.state.config?.dpi_enabled ? (store.state.config.capture_mode || "default") : "disabled");
const captureMode = computed(() => store.state.config?.capture_mode || "default");
const helperState = computed(() => store.state.coreState?.dpi_running ? "Running" : value.value === "disabled" ? "Disabled" : "Stopped / pending restart");
const assets = computed(() => store.state.diagnostics?.assets || []);
function shortHash(hash) { return hash ? `${hash.slice(0, 12)}…` : "—"; }
async function changeMode(event) { const selected = event.target.value; const enabled = selected !== "disabled"; const captureMode = enabled ? selected : (store.state.config?.capture_mode || "default"); try { await store.dispatch("setDPI", { enabled, captureMode }); } catch { event.target.value = value.value; } }
</script>
<style scoped>.ok { color:#36b37e; }.bad { color:#ff6258; }</style>
