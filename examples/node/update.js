#!/usr/bin/env node
// Run wml-update and consume its NDJSON event stream.
// Usage: node update.js updater.json 1.4.2 [path/to/wml-update]

"use strict";

const { spawn } = require("node:child_process");
const readline = require("node:readline");

const [config, current, binaryArg] = process.argv.slice(2);
if (!config || !current) {
  console.error("usage: node update.js updater.json 1.4.2 [path/to/wml-update]");
  process.exit(2);
}

const binary = binaryArg || process.env.WML_UPDATE || "wml-update";
const child = spawn(
  binary,
  ["--config", config, "--json", "update", "--current-version", current],
  { stdio: ["ignore", "pipe", "inherit"] }
);

const rl = readline.createInterface({ input: child.stdout });
let terminal = false;

rl.on("line", (line) => {
  if (!line.trim()) return;
  let event;
  try {
    event = JSON.parse(line);
  } catch {
    console.error("invalid JSON from updater:", line);
    child.kill();
    return;
  }

  switch (event.type) {
    case "progress":
      if (event.phase === "download") {
        console.log(`downloading: ${(event.percent || 0).toFixed(0)}%`);
      }
      break;
    case "warning":
      console.error(`warning: ${event.message}`);
      break;
    case "log":
      console.log(`[${event.phase || ""}] ${event.message}`);
      break;
    case "error":
      console.error(`update failed [${event.error?.code}]: ${event.error?.message}`);
      terminal = true;
      break;
    case "result":
      console.log(JSON.stringify(event.data, null, 2));
      terminal = true;
      break;
  }
});

child.on("error", (err) => {
  console.error("failed to start updater:", err.message);
  process.exitCode = 1;
});

child.on("close", (code) => {
  if (code !== 0) process.exitCode = code;
  else if (!terminal) {
    console.error("updater exited without a terminal event");
    process.exitCode = 1;
  }
});
