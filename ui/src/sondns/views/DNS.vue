<template>
  <section>
    <div class="sondns-card">
      <div class="sondns-card-title">DNS provider <span class="sondns-card-subtitle">hot-swap via service IPC</span></div>
      <div class="sondns-row"><div><div class="sondns-label">Resolver</div><span class="sondns-help">Select the encrypted DNS upstream used by SonDNS.</span></div><select class="sondns-select" :value="provider" @change="changeProvider"><option>SonCMS</option><option>NextDNS</option></select></div>
      <div v-if="pending" class="sondns-card-subtitle pending">{{ pending }} is pending until the service reconnects.</div>
    </div>
    <div class="sondns-card">
      <div class="sondns-card-title">Active endpoint</div>
      <div v-if="upstream" class="sondns-code">{{ upstream.url }}<br><span class="sondns-muted">{{ upstream.type }} · bootstrap {{ upstream.bootstrap?.length || 0 }} addresses</span></div>
      <div v-else class="sondns-empty">Service is offline. Start it to read the current endpoint.</div>
    </div>
    <div class="sondns-card">
      <div class="sondns-card-title">DNS interception</div>
      <div class="sondns-row"><div><div class="sondns-label">WinDivert</div><span class="sondns-help">Transparent DNS interception stays independent from DPI bypass.</span></div><span class="sondns-muted">{{ interception }}</span></div>
      <div class="sondns-row"><div><div class="sondns-label">Local listener</div><span class="sondns-help">Requests are served on the configured loopback address.</span></div><span class="sondns-muted">{{ listen }}</span></div>
    </div>
  </section>
</template>
<script setup>
import { computed } from "vue";
import { useStore } from "vuex";
const store = useStore();
const provider = computed(() => store.getters.provider);
const pending = computed(() => store.state.pendingProvider);
const upstream = computed(() => store.state.config?.upstreams?.[0]);
const interception = computed(() => store.state.config ? "Active" : "Offline");
const listen = computed(() => store.state.config?.listen || "127.0.0.1:5300");
async function changeProvider(event) { try { await store.dispatch("setProvider", event.target.value); } catch { event.target.value = provider.value; } }
</script>
<style scoped>.pending { color: #e7a83b; margin-top: 10px; }</style>
