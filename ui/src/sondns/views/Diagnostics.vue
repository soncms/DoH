<template>
  <section>
    <div class="sondns-card">
      <div class="sondns-card-title">Diagnostics <button class="sondns-button secondary" @click="refresh">Refresh</button></div>
      <div class="sondns-row"><span class="sondns-label">UI version</span><span class="sondns-muted">{{ diagnostics.version || "dev" }}</span></div>
      <div class="sondns-row"><span class="sondns-label">Service state</span><span class="sondns-muted">{{ service.state }} · pipe {{ pipeState }}</span></div>
      <div class="sondns-row"><span class="sondns-label">Binary SHA-256</span><span class="sondns-muted sondns-code">{{ diagnostics.binary_sha256 || "—" }}</span></div>
      <div class="sondns-row"><span class="sondns-label">Signer status</span><span class="sondns-muted">{{ diagnostics.signer_status || "unavailable" }}</span></div>
    </div>
    <div class="sondns-card">
      <div class="sondns-card-title">Runtime assets</div>
      <div v-if="assets.length" class="sondns-table-wrap"><table class="sondns-table"><thead><tr><th>Asset</th><th>Expected</th><th>Actual</th><th>State</th></tr></thead><tbody><tr v-for="asset in assets" :key="asset.name"><td>{{ asset.name }}</td><td class="sondns-code">{{ short(asset.expected) }}</td><td class="sondns-code">{{ short(asset.actual) }}</td><td :class="asset.verified ? 'ok' : 'bad'">{{ asset.verified ? "Verified" : asset.error || "Failed" }}</td></tr></tbody></table></div>
      <div v-else class="sondns-empty">No diagnostic response from the service.</div>
    </div>
    <div class="sondns-card">
      <div class="sondns-card-title">Copy diagnostics</div>
      <div class="sondns-code">{{ report }}</div>
      <div class="sondns-actions" style="justify-content:flex-start"><button class="sondns-button secondary" @click="copy">Copy</button><button class="sondns-button secondary" @click="openData">Open data folder</button></div>
    </div>
  </section>
</template>
<script setup>
import { computed } from "vue";
import { useStore } from "vuex";
const store = useStore();
const diagnostics = computed(() => store.state.diagnostics || {});
const assets = computed(() => diagnostics.value.assets || []);
const service = computed(() => store.state.service);
const pipeState = computed(() => store.state.coreState ? "connected" : "offline");
const report = computed(() => JSON.stringify({ service: service.value, state: store.state.coreState, config: store.state.config, diagnostics: diagnostics.value }, null, 2));
function short(value) { return value ? `${value.slice(0, 16)}…` : "—"; }
const refresh = () => store.dispatch("refresh");
const openData = () => store.dispatch("openData");
async function copy() { await navigator.clipboard?.writeText(report.value); store.commit("setLastAction", "Diagnostics copied"); }
</script>
<style scoped>.ok { color:#36b37e; }.bad { color:#ff6258; }</style>
