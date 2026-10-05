<template>
  <section>
    <div class="sondns-card">
      <div class="sondns-card-title">Service</div>
      <div class="sondns-row"><div><div class="sondns-label">Start with Windows</div><span class="sondns-help">Updates SCM startup and the SonDNS tray entry together.</span></div><button class="sondns-switch" :class="{ on: autoStart }" role="switch" :aria-checked="autoStart" @click="toggleAutostart"></button></div>
      <div class="sondns-row"><span class="sondns-label">Current state</span><span class="sondns-muted">{{ serviceState }}</span></div>
      <div class="sondns-actions" style="justify-content:flex-start"><button class="sondns-button secondary" @click="action('restart')">Restart</button><button class="sondns-button secondary" @click="action('stop')">Stop</button><button v-if="!exists" class="sondns-button" @click="action('install')">Install & start</button></div>
    </div>
    <div class="sondns-card">
      <div class="sondns-card-title">Appearance</div>
      <div class="sondns-row"><div><div class="sondns-label">Theme</div><span class="sondns-help">Uses shared light/dark tokens.</span></div><select class="sondns-select" :value="theme" @change="setTheme"><option value="system">System</option><option value="light">Light</option><option value="dark">Dark</option></select></div>
      <div class="sondns-row"><div><div class="sondns-label">Language</div><span class="sondns-help">UI language preference.</span></div><select class="sondns-select" :value="language" @change="setLanguage"><option value="en">English</option><option value="vi">Tiếng Việt</option></select></div>
      <div class="sondns-row"><div><div class="sondns-label">Close to tray</div><span class="sondns-help">Closing the window keeps the service running.</span></div><button class="sondns-switch" :class="{ on: closeToTray }" @click="save({ closeToTray: !closeToTray })"></button></div>
    </div>
    <div class="sondns-card">
      <div class="sondns-card-title">Data</div>
      <div class="sondns-row"><span class="sondns-label">Config and log directory</span><button class="sondns-button secondary" @click="openData">Open folder</button></div>
      <div class="sondns-card-subtitle" style="margin-top:10px">Core config: %ProgramData%\\SonDNS\\sondns.json<br>UI state: %LOCALAPPDATA%\\SonDNS\\ui.json</div>
    </div>
    <div class="sondns-card">
      <div class="sondns-card-title">About &amp; legal</div>
      <div class="sondns-row"><span class="sondns-label">SonDNS</span><span class="sondns-muted">{{ version }}</span></div>
      <div class="sondns-card-subtitle" style="margin-top:10px">SonDNS is distributed under GPLv3. This UI includes components and visual styling ported from IVPN Desktop; the complete notices and source references are included with the installation.</div>
      <div class="sondns-actions" style="justify-content:flex-start"><button class="sondns-button secondary" @click="openData">Open legal files</button></div>
    </div>
  </section>
</template>
<script setup>
import { computed } from "vue";
import { useStore } from "vuex";
const store = useStore();
const autoStart = computed(() => !!store.state.service.auto_start);
const exists = computed(() => !!store.state.service.exists);
const serviceState = computed(() => store.state.service.state || "unknown");
const theme = computed(() => store.state.ui.theme || "system");
const language = computed(() => store.state.ui.language || "en");
const closeToTray = computed(() => store.state.ui.closeToTray !== false);
const version = computed(() => store.state.diagnostics?.version || "development");
const toggleAutostart = () => store.dispatch("setAutostart", !autoStart.value);
const action = (command) => store.dispatch("serviceAction", command);
const save = (patch) => store.dispatch("saveUI", patch);
const setTheme = (event) => save({ theme: event.target.value });
const setLanguage = (event) => save({ language: event.target.value });
const openData = () => store.dispatch("openData");
</script>

