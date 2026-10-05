const api = () => window.sondns;

export async function request(op, payload = {}) {
  const response = await api().request({ op, ...payload });
  if (!response?.ok) {
    const error = new Error(response?.error || "SonDNS request failed");
    error.code = response?.code || "IPC_ERROR";
    throw error;
  }
  return response;
}

export const service = (command, args = []) => api().service(command, args);
export const getUIState = () => api().getUIState();
export const setUIState = (state) => api().setUIState(state);
export const windowAction = (action) => api().window(action);
export const openData = () => api().openData();
export const saveLog = (content) => api().saveLog(content);

export const providerUpstream = (provider) => provider === "NextDNS"
  ? {
      name: "NextDNS",
      type: "DNS over HTTPS",
      url: "https://dns.nextdns.io/2e2e3c",
      bootstrap: ["45.90.28.0:443", "45.90.30.0:443", "[2a07:a8c0::]:443", "[2a07:a8c1::]:443"],
    }
  : {
      name: "SonCMS",
      type: "DNS over HTTPS",
      url: "https://dns.soncms.net/dns-query/sondns",
    };
