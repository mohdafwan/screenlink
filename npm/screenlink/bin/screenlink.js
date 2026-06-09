#!/usr/bin/env node
"use strict";

// screenlink is a single npm package that bundles a prebuilt Go binary for every
// supported platform under vendor/<platform>-<arch>/. This launcher picks the one
// matching the current machine and hands off to it (forwarding args, stdio, exit
// code, and signals). Node is only the delivery mechanism, not a runtime dep.

const path = require("path");
const fs = require("fs");
const { spawn } = require("child_process");

const platform = process.platform; // 'linux' | 'darwin' | 'win32' | ...
const arch = process.arch; // 'x64' | 'arm64' | ...
const binName = platform === "win32" ? "screenlink.exe" : "screenlink";
const binPath = path.join(__dirname, "..", "vendor", `${platform}-${arch}`, binName);

if (!fs.existsSync(binPath)) {
  console.error(
    `screenlink: no bundled binary for ${platform}-${arch}.\n` +
      `  Supported: linux-x64, linux-arm64, darwin-x64, darwin-arm64, win32-x64.\n` +
      `  Build from source:  go install github.com/mohdafwan/screenlink@latest`
  );
  process.exit(1);
}

const child = spawn(binPath, process.argv.slice(2), { stdio: "inherit" });

for (const sig of ["SIGINT", "SIGTERM", "SIGHUP"]) {
  process.on(sig, () => {
    if (!child.killed) child.kill(sig);
  });
}

child.on("error", (err) => {
  console.error(`screenlink: failed to launch binary: ${err.message}`);
  process.exit(1);
});

child.on("exit", (code, signal) => {
  if (signal) process.kill(process.pid, signal);
  else process.exit(code === null ? 1 : code);
});
