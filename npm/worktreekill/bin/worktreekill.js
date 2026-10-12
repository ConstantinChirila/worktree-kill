#!/usr/bin/env node
// Launches the prebuilt Go binary for this platform, shipped in vendor/.
"use strict";

const { spawn } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");
const { exeName, platformDir } = require("../lib/platform.js");

const vendor = path.join(__dirname, "..", "vendor");
const binary = path.join(vendor, platformDir(process.platform, process.arch), exeName(process.platform));
if (!fs.existsSync(binary)) {
	const available = fs.existsSync(vendor) ? fs.readdirSync(vendor).join(", ") : "none";
	console.error(
		`worktreekill: no prebuilt binary for ${process.platform}-${process.arch} (available: ${available}).\n` +
			"Please open an issue: https://github.com/ConstantinChirila/worktree-kill/issues",
	);
	process.exit(1);
}

// Package tarballs don't reliably keep the executable bit on non-"bin" files.
if (process.platform !== "win32") {
	try {
		fs.accessSync(binary, fs.constants.X_OK);
	} catch {
		try {
			fs.chmodSync(binary, 0o755);
		} catch {
			// Read-only install: fall through and let spawn report the error.
		}
	}
}

const child = spawn(binary, process.argv.slice(2), { stdio: "inherit" });

// Pass signals sent to the launcher on to the binary. Ctrl+C in a terminal
// already reaches both, so the launcher only has to survive it and wait.
const signals = ["SIGINT", "SIGTERM", "SIGHUP"];
for (const signal of signals) {
	process.on(signal, () => {
		if (signal !== "SIGINT") child.kill(signal);
	});
}

child.on("error", (err) => {
	console.error(`worktreekill: failed to start ${binary}: ${err.message}`);
	process.exit(1);
});

child.on("exit", (code, signal) => {
	if (signal) {
		// Exit the same way, so the shell sees the signal: drop the handlers
		// above so the default action (terminate) runs.
		for (const s of signals) process.removeAllListeners(s);
		process.kill(process.pid, signal);
		return;
	}

	process.exit(code ?? 1);
});
