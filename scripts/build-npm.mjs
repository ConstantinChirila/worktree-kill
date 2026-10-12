#!/usr/bin/env node
// Builds the npm package into dist/npm/worktreekill: the launcher plus one
// prebuilt Go binary per platform in vendor/<os>-<arch>/.
//
// Usage: node scripts/build-npm.mjs <version>
// Publishing is a separate, manual step (see the printed command).

import { execFileSync } from "node:child_process";
import { chmodSync, cpSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import platform from "../npm/worktreekill/lib/platform.js";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const out = join(root, "dist", "npm", "worktreekill");

// npm's process.platform/arch names mapped to Go's GOOS/GOARCH.
const targets = [
	{ os: "darwin", cpu: "arm64", goos: "darwin", goarch: "arm64" },
	{ os: "darwin", cpu: "x64", goos: "darwin", goarch: "amd64" },
	{ os: "linux", cpu: "x64", goos: "linux", goarch: "amd64" },
	{ os: "linux", cpu: "arm64", goos: "linux", goarch: "arm64" },
	{ os: "win32", cpu: "x64", goos: "windows", goarch: "amd64" },
];

const version = process.argv[2];
if (!/^\d+\.\d+\.\d+(-[\w.]+)?$/.test(version ?? "")) {
	console.error("usage: node scripts/build-npm.mjs <version>   (e.g. 0.1.0)");
	process.exit(1);
}

rmSync(join(root, "dist", "npm"), { recursive: true, force: true });
cpSync(join(root, "npm", "worktreekill"), out, { recursive: true });
cpSync(join(root, "README.md"), join(out, "README.md"));

const pkg = JSON.parse(readFileSync(join(root, "npm", "worktreekill", "package.json"), "utf8"));
pkg.version = version;
writeFileSync(join(out, "package.json"), JSON.stringify(pkg, null, 2) + "\n");

for (const t of targets) {
	const dir = join(out, "vendor", platform.platformDir(t.os, t.cpu));
	const exe = join(dir, platform.exeName(t.os));
	mkdirSync(dir, { recursive: true });

	console.log(`building ${t.os}-${t.cpu}`);
	execFileSync("go", ["build", "-trimpath", "-ldflags", `-s -w -X main.version=${version}`, "-o", exe, "."], {
		cwd: root,
		stdio: "inherit",
		env: { ...process.env, GOOS: t.goos, GOARCH: t.goarch, CGO_ENABLED: "0" },
	});
	chmodSync(exe, 0o755);
}

console.log(`\nBuilt dist/npm/worktreekill ${version}. To publish:`);
console.log("  npm publish dist/npm/worktreekill --access public");
