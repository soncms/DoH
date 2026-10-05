import { contextBridge, ipcRenderer } from "electron";

const api = {
  request: (request) => ipcRenderer.invoke("sondns:pipe", request),
  service: (command, args = []) => ipcRenderer.invoke("sondns:service", command, args),
  getUIState: () => ipcRenderer.invoke("sondns:ui-state:get"),
  setUIState: (state) => ipcRenderer.invoke("sondns:ui-state:set", state),
  window: (action) => ipcRenderer.invoke("sondns:window", action),
  openData: () => ipcRenderer.invoke("sondns:open-data"),
  saveLog: (content) => ipcRenderer.invoke("sondns:save-log", content),
  quit: () => ipcRenderer.send("sondns:quit"),
  onNavigate: (callback) => ipcRenderer.on("sondns:navigate", (_event, route) => callback(route)),
};

contextBridge.exposeInMainWorld("sondns", api);
