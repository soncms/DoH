// SonDNS platform style loader. The IVPN-derived SCSS is retained under GPLv3
// and is loaded from the platform-specific entry before the renderer bundle.
console.log("[ ] Preparing SonDNS UI build...");
const os = require("node:os");
const fs = require("node:fs");
const path = require("node:path");
const target = path.join(__dirname, "src", "main_style.js");
const source = path.join(__dirname, "src", `main_style_${os.platform()}.js`);
if (!fs.existsSync(source)) throw new Error(`Unsupported platform style loader: ${source}`);
fs.copyFileSync(source, target);
console.log(`[ ] Using style loader: ${source}`);
