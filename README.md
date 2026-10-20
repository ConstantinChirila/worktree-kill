<div align="center">

<pre>
                █     █                  █    ▀ █  █ 
█   █ ▄▀▀▄ █▄▀▀ █ ▄▀ ▀█▀▀ █▄▀▀ ▄▀▀▄ ▄▀▀▄ █ ▄▀ █ █  █ 
█ █ █ █  █ █    █▀▄   █   █    █▀▀▀ █▀▀▀ █▀▄  █ █  █ 
 ▀ ▀   ▀▀  ▀    ▀  ▀   ▀▀ ▀     ▀▀▀  ▀▀▀ ▀  ▀ ▀  ▀  ▀
</pre>

**Your AI agents left 40 worktrees behind. Get your disk back.**

Find every git worktree on your machine, see what each one costs, delete the ones you're done with.<br>
Like [npkill](https://github.com/voidcosmos/npkill), but for worktrees.

[![CI](https://github.com/ConstantinChirila/worktree-kill/actions/workflows/ci.yml/badge.svg)](https://github.com/ConstantinChirila/worktree-kill/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
![Platforms](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)

```sh
npx worktreekill
```

</div>

```
worktreekill  scanned ~ in 3.2s  23 worktrees · 41.7 GB  3 selected (12.4 GB)
  TOOL      REPO    BRANCH          PATH                            SIZE LAST USED STATUS
◉ Claude    webapp  fix-login-flow  …de/worktrees/fix-login-flow  4.8 GB   3mo ago clean
◉ Cursor    api     refactor-auth   ~/.cursor/worktrees/api/k2x9  4.1 GB   2mo ago clean
◉ Codex     webapp  feat/dark-mode  …codex/worktrees/a81f/webapp  3.5 GB    7w ago 1 unpushed
○ Conductor shop    lisbon          ~/conductor/shop/lisbon       2.9 GB    3w ago 4 changed
○ Claude    docs    (3f9a2c1)       …ocs/.claude/worktrees/spike  1.2 GB    9d ago locked
○ git       infra   hotfix-dns      ~/code/infra-hotfix           812 MB    2d ago you are here
○ T3        webapp  t3-onboarding   …worktrees/webapp/onboarding  604 MB    5h ago 2 changed · 3 unpushed

⚠ 1 with ignored files (will be lost: .env)
Delete 3 worktrees (12.4 GB)? [y/N]
```

## Why

Every agent run gets its own worktree: a full checkout, usually with its own `node_modules`. Claude Code, Cursor, Codex, Conductor and T3 each stash them somewhere different, mostly in hidden folders, and `git worktree list` only shows one repo at a time. They pile up quietly, a few GB each.

worktreekill finds all of them in one pass and puts the biggest, oldest ones at the top.

## Highlights

- **Finds everything.** Every worktree in every repo under your home folder, plus each tool's hidden worktree folders, plus orphans whose repo is already gone.
- **Shows what it costs.** Size on disk, when you last touched it, and whether it holds work you haven't committed or pushed.
- **Won't eat your work.** Nothing is deleted without a `y`. First you get a warning for anything you'd lose: uncommitted changes, commits on no branch, locked worktrees, and ignored files like `.env` that plain `git worktree remove` deletes without a word. Worktrees go through `git worktree remove`, so git checks too.
- **Fast.** Folders are read in parallel and results stream in while the scan runs.
- **Safe on repos you don't trust.** Repo hooks, fsmonitor, filters and network fetches are switched off for every git command it runs ([details](#security)).
- **Scriptable.** `--list` prints a table, `--json` prints JSON.

| | `rm -rf` | `git worktree remove` | **worktreekill** |
|---|:---:|:---:|:---:|
| Finds worktrees across all repos and tools | ✗ | ✗ | ✓ |
| Shows size and last used | ✗ | ✗ | ✓ |
| Warns before deleting ignored files like `.env` | ✗ | ✗ | ✓ |
| Refuses dirty worktrees unless you confirm | ✗ | ✓ | ✓ |
| Cleans up git's worktree entry | ✗ | ✓ | ✓ |

## What it shows

| Column    | Meaning |
|-----------|---------|
| TOOL      | Which tool made it, guessed from its location: Claude, Cursor, Codex, Conductor, T3, or plain git |
| SIZE      | Roughly the space deleting it frees. Hard-linked files (pnpm links packages from its store) are left out, since deleting them frees nothing. On Windows they are counted |
| LAST USED | Newest of the last commit and the last file change |
| STATUS    | `clean`, `N changed` (uncommitted), `N unpushed` (commits on no remote branch and not on `main`/`master`; if the repo has neither, not on the main checkout's branch), `locked`, `dir missing`, `orphaned`, `you are here` |

Results appear as they're found. Size and status fill in as they're calculated.

Under the list, the highlighted row is shown in full: its whole branch name and path, plus what the table cuts off or leaves out (lock reason, git errors, ignored files, repo). They hide only when the terminal is too short to also show three rows.

## Where it looks

With no arguments it scans your home folder. It skips hidden folders, `node_modules`, `bower_components`, `Pods`, `DerivedData`, Go's module cache (`~/go/pkg`), and `~/Library`, `~/Applications`, `~/Movies`, `~/Music`, `~/Pictures`, `~/OrbStack`, `~/miniconda3`, `~/anaconda3`, `~/miniforge3`. It also scans the places tools keep worktrees:

- `~/.claude-worktrees`, `<repo>/.claude/worktrees` (Claude Code)
- `~/.cursor/worktrees` (Cursor)
- `~/.codex/worktrees`, `$CODEX_HOME/worktrees` (Codex)
- `~/conductor`, `<repo>/.conductor` (Conductor)
- `~/.t3/worktrees` (T3 Code)

Worktrees registered in any repo it finds are listed wherever they live on disk. Inside a repo, only `.claude/worktrees` and `.conductor` are walked (to find orphans git has forgotten), unless the repo is a folder you scan: a home folder that is a dotfiles repo is still scanned in full. To scan specific folders instead, pass them: `npx worktreekill ~/code ~/work`.

## Keys

| Key        | Action |
|------------|--------|
| ↑ ↓ / j k  | Move |
| pgup pgdn  | Move a page |
| g G / home end | Jump to top or bottom |
| space      | Select |
| a          | Select all shown rows (press again to clear them). Selections the filter hides are kept |
| enter / d / delete / backspace | Delete selected (or the current row), after confirming |
| b          | Toggle also deleting the branch (`git branch -d`, which refuses unmerged branches) |
| s          | Sort: largest, oldest, repo |
| /          | Filter by path, branch, repo or tool |
| esc        | Clear the filter (also while typing it), or quit |
| q / ctrl+c | Quit (waits for deletions in progress) |

## How deletion works

- A worktree can't be deleted until its git status has loaded, so the prompt can warn about everything that would be lost: uncommitted changes, commits on no branch (detached HEAD), ignored files that can't simply be rebuilt (like `.env`), submodules, locks, and selected rows the filter hides. The ignored-files warning leaves out what tools regenerate: dependency folders (`node_modules`, `vendor`, `.venv`, `Pods` and similar), build output (`dist`, `build`, `out`, `target`, `.next` and similar), caches (`.cache`, `__pycache__`, `.pytest_cache` and similar), `*.log`, `*.pyc` and `.DS_Store`.
- Normal worktrees are removed with `git worktree remove`. If one has uncommitted changes, `--force` is used only after you confirm, and only if exactly the same changes are still there when deletion starts.
- Populated submodules and embedded repos (repos committed inside the repo): git refuses to remove a worktree with these unless forced. The prompt warns, and after you confirm `--force` is used, which deletes them along with their changes and unpushed commits. Those aren't checked. A new worktree's submodules are empty, so this only applies once you've run `git submodule update` in it.
- Locked worktrees (Claude Code locks worktrees while an agent runs) get a warning. They are unlocked and then removed, so git still checks for uncommitted changes. If removal fails, the lock is put back.
- `dir missing`: the folder is already gone, so only git's entry for it is removed. If it is also locked (git's way of keeping worktrees on removable drives), it is left alone: run `git worktree unlock` first.
- `orphaned`: the repo is gone, so git can't remove it and the folder is deleted directly. Its contents can't be checked for uncommitted work. A worktree only counts as orphaned if the folder that held the repo still exists. When that is missing too (an unmounted drive, a devcontainer path), it isn't listed. A stray `.git` file pointing inside its own folder doesn't count either. Your home folder, the scanned folders, anything containing them and folders directly in your home folder (like `~/Downloads`) are never deleted this way. Neither is an orphan that holds another repo or worktree. Each orphan is checked again just before it is deleted, through the same folder handle that deletes it, so swapping in a symlink can't redirect the delete.
- Branches are kept unless you turn on branch deletion with `b`.

## Scripting

```sh
npx worktreekill --list          # table (also used when output is piped)
npx worktreekill --json          # JSON, sorted by size
npx worktreekill --version
```

In JSON, `state` is `ok`, `missing` or `orphaned`. If git status failed, `gitError` says why and the change counts are 0 because they're unknown.

## Security

worktreekill runs git in every worktree it finds, and git reads each repo's own config. A repo extracted from an archive (cloning never copies config) could use that config to make git run a program. worktreekill switches off the settings its git commands would act on:

- `core.fsmonitor`, which `git status` runs every time.
- Filters (`filter.<name>.clean` and friends) defined in the repo's own config, including files it includes. Filters from your global or system config, like git-lfs, keep working. A repo that sets up a filter only in its own config may show files as changed when they aren't.
- Signature checks in `git log` (`log.showSignature` with `gpg.program`).
- Hooks (`core.hooksPath` and `.git/hooks`), which `git branch -d` would run.
- Network access, through an empty `GIT_ALLOW_PROTOCOL`, which overrides any `protocol.*.allow` the repo sets. So a "partial clone" repo can't fetch missing objects through its own `core.sshCommand`, `ext::` remote or remote helper. Such a repo shows a git error instead.
- `GIT_DIR`, `GIT_WORK_TREE` and similar variables, if worktreekill is started from inside a git hook.

Submodules have their own config, so `git status` skips them: changes inside a submodule aren't counted, and the delete prompt says when a worktree has populated submodules. This needs git 2.26 or newer; with older git, status fails and those worktrees can't be deleted from the tool.
