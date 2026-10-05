const path = require("node:path");
const base = require("./electron-builder.config.js");
base.directories = { ...base.directories, output: path.join(__dirname, "..", "dist", "electron-installer") };
base.win = { ...base.win, target: ["nsis"] };
module.exports = base;
