import { createStore } from "vuex";
import { getUIState, openData, providerUpstream, request, service, setUIState } from "./api";

const initialUI = { theme: "system", language: "en", closeToTray: true, lastRoute: "/" };

export default createStore({
  strict: true,
  state: {
    service: { exists: false, state: "unknown", auto_start: false, binary_path: "" },
    coreState: null,
    config: null,
    diagnostics: null,
    pendingProvider: "",
    entries: [],
    overwrittenRows: 0,
    logVersion: 0,
    ui: { ...initialUI },
    loading: false,
    error: "",
    lastAction: "",
  },
  getters: {
    isRunning: (state) => state.service.state === "running" && state.coreState?.dns_running === true,
    provider: (state) => state.pendingProvider || state.config?.provider || "SonCMS",
    dpiLabel: (state) => {
      if (!state.config?.dpi_enabled) return "Disabled";
      return ({ default: "Default", tcp: "TCP", "tcp+udp": "TCP + QUIC UDP" })[state.config.capture_mode] || state.config.capture_mode;
    },
  },
  mutations: {
    setLoading(state, value) { state.loading = value; },
    setError(state, value) { state.error = value || ""; },
    setService(state, value) { state.service = value || state.service; },
    setCoreState(state, value) { state.coreState = value; },
    setConfig(state, value) { state.config = value; },
    setDiagnostics(state, value) { state.diagnostics = value; },
    setPendingProvider(state, value) { state.pendingProvider = value || ""; },
    setLog(state, value) {
      state.entries = value?.entries || [];
      state.logVersion = value?.version || 0;
      state.overwrittenRows = value?.overwritten_rows || 0;
    },
    setUI(state, value) { state.ui = { ...state.ui, ...(value || {}) }; },
    setLastAction(state, value) { state.lastAction = value || ""; },
  },
  actions: {
    async refresh({ state, commit }) {
      commit("setLoading", true);
      const [ui, serviceResult, stateResult, configResult, diagnosticsResult, logResult] = await Promise.all([
        getUIState().catch(() => initialUI),
        service("status", ["--json"]).catch(() => ({ exists: false, state: "unknown", auto_start: false, binary_path: "" })),
        request("get_state").catch(() => null),
        request("get_config").catch(() => null),
        request("diagnostics").catch(() => null),
        request("snapshot").catch(() => null),
      ]);
      commit("setUI", ui);
      commit("setService", serviceResult);
      if (stateResult) commit("setCoreState", stateResult.state);
      if (configResult) commit("setConfig", configResult.config);
      if (diagnosticsResult) commit("setDiagnostics", diagnosticsResult.diagnostics);
      if (logResult) commit("setLog", logResult);
      if (state.pendingProvider && serviceResult?.state === "running" && state.coreState) {
        try {
          await request("set_upstream", { upstreams: [providerUpstream(state.pendingProvider)] });
          commit("setLastAction", `${state.pendingProvider} applied after service reconnect`);
          commit("setPendingProvider", "");
        } catch {
          // Keep the pending choice visible until the service accepts it.
        }
      }
      commit("setLoading", false);
      return { serviceResult, stateResult, configResult };
    },
    async clearLog({ commit }) {
      const response = await request("clear_log");
      commit("setLog", { entries: [], version: response.version });
      commit("setLastAction", "Log cleared");
    },
    async setProvider({ state, dispatch, commit }, provider) {
      commit("setError", "");
      if (state.service.state !== "running" || !state.coreState) {
        commit("setPendingProvider", provider);
        commit("setLastAction", `${provider} pending until the service is running`);
        return;
      }
      try {
        const response = await request("set_upstream", { upstreams: [providerUpstream(provider)] });
        commit("setPendingProvider", "");
        commit("setLastAction", response.needs_restart ? "Provider saved; restart required" : "Provider applied");
        await dispatch("refresh");
      } catch (error) {
        if (error.code === "IPC_ERROR" || /pipe|socket|disconnect|not found/i.test(error.message)) {
          commit("setPendingProvider", provider);
          commit("setLastAction", `${provider} pending until the service is running`);
          return;
        }
        throw error;
      }
    },
    async setDPI({ state, dispatch, commit }, { enabled, captureMode }) {
      const previous = state.config ? { enabled: state.config.dpi_enabled, captureMode: state.config.capture_mode } : null;
      commit("setError", "");
      try {
        const response = await request("set_dpi", { enabled, capture_mode: captureMode });
        if (response.needs_restart) await service("restart");
        await dispatch("refresh");
      } catch (error) {
        if (previous) {
          try {
            await request("set_dpi", { enabled: previous.enabled, capture_mode: previous.captureMode });
            await service("restart");
          } catch { /* keep original error */ }
        }
        commit("setError", error.message);
        throw error;
      }
    },
    async serviceAction({ dispatch, commit }, command) {
      commit("setError", "");
      try {
        await service(command);
        await new Promise((resolve) => setTimeout(resolve, command === "install" ? 1400 : 500));
        commit("setLastAction", `${command} requested`);
        await dispatch("refresh");
      } catch (error) {
        commit("setError", error.message);
        throw error;
      }
    },
    async setAutostart({ dispatch, commit }, enabled) {
      commit("setError", "");
      try {
        await service("autostart", [enabled ? "--enable" : "--disable"]);
        await new Promise((resolve) => setTimeout(resolve, 500));
        commit("setLastAction", enabled ? "Start with Windows enabled" : "Start with Windows disabled");
        await dispatch("refresh");
      } catch (error) {
        commit("setError", error.message);
        throw error;
      }
    },
    async saveUI({ state, commit }, patch) {
      const ui = await setUIState({ ...state.ui, ...patch });
      commit("setUI", ui);
    },
    openData() { return openData(); },
  },
});
