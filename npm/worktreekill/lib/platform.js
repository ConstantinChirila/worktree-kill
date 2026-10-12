// Platform package naming, shared by the launcher and scripts/build-npm.mjs.
"use strict";

// Scoped, so nobody else can publish a package the launcher would run.
const scope = "@worktreekill";

/** npm package holding the binary for an npm os/cpu pair, like "@worktreekill/darwin-arm64". */
exports.packageName = (os, cpu) => `${scope}/${os}-${cpu}`;

/** File name of the binary inside that package's bin folder. */
exports.exeName = (os) => (os === "win32" ? "worktreekill.exe" : "worktreekill");
