<template>
  <section>
    <div class="sondns-card">
      <div class="sondns-card-title"><span>DNS requests</span><div><button class="sondns-button secondary" @click="paused = !paused">{{ paused ? "Resume" : "Pause" }}</button> <button class="sondns-button secondary" @click="refresh" :disabled="paused">Refresh</button> <button class="sondns-button secondary" @click="clear">Clear</button> <button class="sondns-button secondary" @click="save">Save</button></div></div>
      <div class="sondns-row" style="border-top:0"><input v-model="filter" class="sondns-select" style="width:100%" placeholder="Filter domain, result or type" /></div>
      <div v-if="filtered.length" class="sondns-table-wrap"><table class="sondns-table"><thead><tr><th>Time</th><th>Domain</th><th>Type</th><th>Result</th><th>Latency</th></tr></thead><tbody><tr v-for="entry in filtered" :key="entry.id" @click="selected = entry"><td>{{ time(entry.ts) }}</td><td class="domain" :title="entry.domain">{{ entry.domain }}</td><td>{{ entry.type }}</td><td>{{ entry.result || entry.rcode }}</td><td>{{ entry.latency_ms }} ms</td></tr></tbody></table></div>
      <div v-else class="sondns-empty">No DNS requests in memory.</div>
      <div class="sondns-card-subtitle" style="margin-top:9px">{{ store.state.entries.length }} rows · ring overwrites {{ store.state.overwrittenRows }} older rows</div>
    </div>
      <div v-if="selected" class="sondns-card">
      <div class="sondns-card-title">Request details <button class="sondns-button secondary" @click="selected = null">Close</button></div>
      <div class="sondns-code">{{ JSON.stringify(selected, null, 2) }}</div>
      <div class="sondns-actions" style="justify-content:flex-start"><button class="sondns-button secondary" @click="copy(selected.domain)">Copy domain</button><button class="sondns-button secondary" @click="copy((selected.answer_ips || []).join('\n'))">Copy IPs</button></div>
    </div>
  </section>
</template>
<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useStore } from "vuex";
import { saveLog } from "../api";
const store = useStore();
const filter = ref("");
const paused = ref(false);
const selected = ref(null);
const filtered = computed(() => { const q = filter.value.trim().toLowerCase(); if (!q) return [...store.state.entries].reverse(); return [...store.state.entries].reverse().filter((entry) => [entry.domain, entry.result, entry.rcode, entry.type, entry.protocol].join(" ").toLowerCase().includes(q)); });
const time = (value) => value ? new Date(value).toLocaleTimeString() : "—";
const refresh = () => { if (!paused.value) store.dispatch("refresh"); };
const clear = () => store.dispatch("clearLog");
const copy = (value) => navigator.clipboard?.writeText(value || "");
const save = () => saveLog(JSON.stringify(store.state.entries, null, 2));
let timer;
onMounted(() => { timer = setInterval(() => { if (!paused.value) refresh(); }, 5000); });
onBeforeUnmount(() => clearInterval(timer));
</script>
