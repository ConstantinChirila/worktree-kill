#!/usr/bin/env node
// Launches the prebuilt Go binary from the platform package npm installed
// alongside this one (via optionalDependencies, filtered by os/cpu).
"use strict";

const { spawnSync } = require("node:child_process");
const fs = require("node:fs");

const pkg = `worktreekill-${process.platform}-${process.arch}`;
const exe = process.platform === "win32" ? "worktreekill.exe" : "worktreekill";

let binary;
try {
	binary = require.resolve(`${pkg}/bin/${exe}`);
} catch {
	console.error(
		`worktreekill: no prebuilt binary for ${process.platform}-${process.arch}.\n` +
			`Expected the optional dependency "${pkg}". If you installed with --no-optional ` +
			`or --omit=optional, reinstall without it.`,
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

const result = spawnSync(binary, process.argv.slice(2), { stdio: "inherit" });
if (result.error) {
	console.error(`worktreekill: failed to start ${binary}: ${result.error.message}`);
	process.exit(1);
}

if (result.signal) {
	process.kill(process.pid, result.signal);
}

process.exit(result.status ?? 1);
