#!/usr/bin/env node
// Builds the npm packages into dist/npm/:
//   worktreekill                 launcher, depends on all platform packages
//   worktreekill-<os>-<arch>     one prebuilt Go binary each
//
// Usage: node scripts/build-npm.mjs <version>
// Publishing is a separate, manual step (see the printed commands).

import { execFileSync } from "node:child_process";
import { chmodSync, cpSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const out = join(root, "dist", "npm");

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

rmSync(out, { recursive: true, force: true });
const launcher = JSON.parse(readFileSync(join(root, "npm", "worktreekill", "package.json"), "utf8"));
launcher.version = version;
launcher.optionalDependencies = {};

for (const t of targets) {
	const name = `worktreekill-${t.os}-${t.cpu}`;
	const dir = join(out, name);
	const exe = t.os === "win32" ? "worktreekill.exe" : "worktreekill";
	mkdirSync(join(dir, "bin"), { recursive: true });

	console.log(`building ${name}`);
	execFileSync(
		"go",
		["build", "-trimpath", "-ldflags", `-s -w -X main.version=${version}`, "-o", join(dir, "bin", exe), "."],
		{ cwd: root, stdio: "inherit", env: { ...process.env, GOOS: t.goos, GOARCH: t.goarch, CGO_ENABLED: "0" } },
	);
	chmodSync(join(dir, "bin", exe), 0o755);

	const pkg = {
		name,
		version,
		description: `worktreekill binary for ${t.os}-${t.cpu}. Install "worktreekill" instead.`,
		os: [t.os],
		cpu: [t.cpu],
		repository: launcher.repository,
		files: ["bin"],
		license: launcher.license,
	};
	writeFileSync(join(dir, "package.json"), JSON.stringify(pkg, null, 2) + "\n");
	launcher.optionalDependencies[name] = version;
}

const launcherDir = join(out, "worktreekill");
cpSync(join(root, "npm", "worktreekill"), launcherDir, { recursive: true });
cpSync(join(root, "README.md"), join(launcherDir, "README.md"));
writeFileSync(join(launcherDir, "package.json"), JSON.stringify(launcher, null, 2) + "\n");

console.log(`\nBuilt ${targets.length + 1} packages in dist/npm. To publish (platform packages first):`);
for (const t of targets) console.log(`  npm publish dist/npm/worktreekill-${t.os}-${t.cpu} --access public`);
console.log("  npm publish dist/npm/worktreekill --access public");
