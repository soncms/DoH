import { app, BrowserWindow, Tray, Menu, ipcMain, nativeTheme, nativeImage, shell, dialog } from "electron";
import { spawn } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join, dirname } from "node:path";
import net from "node:net";

let mainWindow;
let tray;
let isQuitting = false;
let closeToTray = true;
let uiStatePath;

const singleInstance = app.requestSingleInstanceLock();
if (!singleInstance) {
  app.quit();
} else {
  app.on("second-instance", () => showWindow());
}

const PIPE_NAME = "\\\\.\\pipe\\SonDNS\\control";

function resolveServicePath() {
  const candidates = [];
  if (process.env.SONDNS_SERVICE_PATH) candidates.push(process.env.SONDNS_SERVICE_PATH);
  if (process.resourcesPath) candidates.push(join(process.resourcesPath, "SonDNS.Service.exe"));
  candidates.push(join(dirname(process.execPath), "SonDNS.Service.exe"));
  candidates.push(join(app.getAppPath(), "..", "dist", "SonDNS.Service.exe"));
  candidates.push(join(app.getAppPath(), "SonDNS.Service.exe"));
  return candidates.find((candidate) => existsSync(candidate)) || candidates[0];
}

function requestPipe(request, timeoutMs = 5000) {
  return new Promise((resolve, reject) => {
    const socket = net.createConnection({ path: PIPE_NAME });
    let buffer = "";
    let finished = false;
    const finish = (fn, value) => {
      if (finished) return;
      finished = true;
      clearTimeout(timer);
      socket.destroy();
      fn(value);
    };
    const timer = setTimeout(() => finish(reject, new Error("SonDNS IPC timeout")), timeoutMs);
    socket.setEncoding("utf8");
    socket.on("connect", () => socket.write(`${JSON.stringify(request)}\n`));
    socket.on("data", (chunk) => {
      buffer += chunk;
      const lineEnd = buffer.indexOf("\n");
      if (lineEnd < 0) return;
      try {
        finish(resolve, JSON.parse(buffer.slice(0, lineEnd)));
      } catch (error) {
        finish(reject, error);
      }
    });
    socket.on("error", (error) => finish(reject, error));
    socket.on("close", () => {
      if (!finished) finish(reject, new Error("SonDNS IPC disconnected"));
    });
  });
}

function runService(command, args = []) {
  const executable = resolveServicePath();
  return new Promise((resolve, reject) => {
    if (!executable || !existsSync(executable)) {
      reject(new Error(`SonDNS.Service.exe not found: ${executable}`));
      return;
    }
    const child = spawn(executable, [command, ...args], {
      windowsHide: true,
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (data) => (stdout += data.toString()));
    child.stderr.on("data", (data) => (stderr += data.toString()));
    child.on("error", reject);
    child.on("close", (code) => {
      if (code === 0) resolve({ code, stdout, stderr });
      else reject(new Error((stderr || stdout || `service exited ${code}`).trim()));
    });
  });
}

function loadUIState() {
  try {
    return JSON.parse(readFileSync(uiStatePath, "utf8"));
  } catch {
    return { theme: "system", language: "en", closeToTray: true, lastRoute: "/", bounds: { width: 760, height: 520 } };
  }
}

function saveUIState(state) {
  mkdirSync(dirname(uiStatePath), { recursive: true });
  writeFileSync(uiStatePath, JSON.stringify(state, null, 2), "utf8");
  return state;
}

function applyTheme(theme) {
  nativeTheme.themeSource = theme === "dark" || theme === "light" ? theme : "system";
}

function showWindow(route) {
  if (!mainWindow || mainWindow.isDestroyed()) return;
  if (route) mainWindow.webContents.send("sondns:navigate", route);
  mainWindow.show();
  mainWindow.focus();
}

function createTray() {
  if (tray) return;
  const icon = join(app.getAppPath(), "..", "assets", "sondns.ico");
  tray = new Tray(existsSync(icon) ? icon : nativeImageFallback());
  tray.setToolTip("SonDNS");
  tray.on("double-click", () => showWindow());
  tray.on("click", () => showWindow());
  tray.setContextMenu(Menu.buildFromTemplate([
    { label: "Open SonDNS", click: () => showWindow() },
    { type: "separator" },
    { label: "Quit", click: () => { isQuitting = true; app.quit(); } },
  ]));
}

function nativeImageFallback() {
  return nativeImage.createEmpty();
}

function createWindow(state) {
  const bounds = state?.bounds || { width: 760, height: 520 };
  mainWindow = new BrowserWindow({
    width: bounds.width || 760,
    height: bounds.height || 520,
    x: Number.isFinite(bounds.x) ? bounds.x : undefined,
    y: Number.isFinite(bounds.y) ? bounds.y : undefined,
    minWidth: 660,
    minHeight: 460,
    maxWidth: 1000,
    maxHeight: 760,
    resizable: true,
    frame: false,
    show: false,
    backgroundColor: "#1c1c1e",
    icon: join(app.getAppPath(), "..", "assets", "sondns.ico"),
    webPreferences: {
      preload: join(__dirname, "../preload/preload.js"),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: false,
    },
  });
  mainWindow.on("close", (event) => {
    if (!isQuitting && closeToTray) {
      event.preventDefault();
      mainWindow.hide();
    }
  });
  const saveBounds = () => saveUIState({ bounds: mainWindow.getBounds() });
  mainWindow.on("moved", saveBounds);
  mainWindow.on("resized", saveBounds);
  mainWindow.on("closed", () => { mainWindow = undefined; });
  const devUrl = process.env.ELECTRON_RENDERER_URL;
  if (devUrl) mainWindow.loadURL(devUrl);
  else mainWindow.loadFile(join(__dirname, "../renderer/index.html"));
  mainWindow.webContents.on("did-finish-load", () => {
    if (process.argv.includes("--tray")) return;
    showWindow();
  });
}

if (singleInstance) app.whenReady().then(() => {
  const localAppData = process.env.LOCALAPPDATA || join(app.getPath("appData"), "..", "Local");
  uiStatePath = join(localAppData, "SonDNS", "ui.json");
  const state = loadUIState();
  closeToTray = state.closeToTray !== false;
  applyTheme(state.theme || "system");
  createWindow(state);
  createTray();
  app.on("activate", () => showWindow());
});

app.on("before-quit", () => { isQuitting = true; });

ipcMain.handle("sondns:pipe", async (_event, request) => {
  const timeout = request?.op === "set_upstream" ? 15000 : 5000;
  return requestPipe(request, timeout);
});
ipcMain.handle("sondns:service", async (_event, command, args) => runService(command, args || []));
ipcMain.handle("sondns:ui-state:get", () => loadUIState());
ipcMain.handle("sondns:ui-state:set", (_event, state) => {
  const next = saveUIState({ ...loadUIState(), ...(state || {}) });
  closeToTray = next.closeToTray !== false;
  applyTheme(next.theme || "system");
  return next;
});
ipcMain.handle("sondns:window", (_event, action) => {
  if (!mainWindow) return;
  if (action === "minimize") mainWindow.minimize();
  else if (action === "close") mainWindow.close();
  else if (action === "show") showWindow();
});
ipcMain.handle("sondns:open-data", async () => {
  const localAppData = process.env.LOCALAPPDATA || join(app.getPath("appData"), "..", "Local");
  return shell.openPath(join(localAppData, "SonDNS"));
});
ipcMain.handle("sondns:save-log", async (_event, content) => {
  const result = await dialog.showSaveDialog(mainWindow, {
    title: "Save SonDNS log",
    defaultPath: "sondns-log.json",
    filters: [{ name: "JSON", extensions: ["json"] }, { name: "All files", extensions: ["*"] }],
  });
  if (result.canceled || !result.filePath) return "";
  writeFileSync(result.filePath, String(content || ""), "utf8");
  return result.filePath;
});
ipcMain.on("sondns:quit", () => { isQuitting = true; app.quit(); });


