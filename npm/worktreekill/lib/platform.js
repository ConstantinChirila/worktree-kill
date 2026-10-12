// Where each platform's binary lives inside the package, shared by the
// launcher and scripts/build-npm.mjs.
"use strict";

/** Folder under vendor/ holding the binary for an npm os/cpu pair, like "darwin-arm64". */
exports.platformDir = (os, cpu) => `${os}-${cpu}`;

/** File name of the binary in that folder. */
exports.exeName = (os) => (os === "win32" ? "worktreekill.exe" : "worktreekill");
