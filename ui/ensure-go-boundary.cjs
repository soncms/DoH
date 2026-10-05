const fs = require("node:fs");
const path = require("node:path");
const boundary = path.join(__dirname, "node_modules", "go.mod");
if (!fs.existsSync(boundary)) fs.writeFileSync(boundary, "module sondns-ui-node-modules\\n\\ngo 1.27\\n", "utf8");
