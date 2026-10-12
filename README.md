# worktreekill

Find every git worktree on your machine and delete the ones you don't need. Like [npkill](https://github.com/voidcosmos/npkill), but for worktrees.

```sh
npx worktreekill
```

AI coding tools create a lot of worktrees, and each one is a full checkout, often with its own `node_modules`. worktreekill finds them, shows how much space each takes and when it was last used, and deletes the ones you pick.

## What it shows

| Column    | Meaning |
|-----------|---------|
| TOOL      | Which tool made it, guessed from its location: Claude, Cursor, Codex, Conductor, T3, or plain git |
| SIZE      | Roughly the space deleting it frees. Hard-linked files (pnpm links packages from its store) are left out, since deleting them frees nothing |
| LAST USED | Newest of the last commit and the last file change |
| STATUS    | `clean`, `N changed` (uncommitted), `N unpushed` (commits on no remote branch and not on `main`/`master`), `locked`, `dir missing`, `orphaned`, `you are here` |

Results appear as they're found. Size and status fill in as they're calculated.

## Where it looks

With no arguments it scans your home folder. It skips hidden folders, `node_modules`, `bower_components`, `Pods`, `DerivedData`, and `~/Library`, `~/Applications`, `~/Movies`, `~/Music`, `~/Pictures`. It also scans the places tools keep worktrees:

- `~/.claude-worktrees`, `<repo>/.claude/worktrees` (Claude Code)
- `~/.cursor/worktrees` (Cursor)
- `~/.codex/worktrees`, `$CODEX_HOME/worktrees` (Codex)
- `~/conductor`, `<repo>/.conductor` (Conductor)
- `~/.t3/worktrees` (T3 Code)

Worktrees registered in any repo it finds are listed wherever they live on disk. Inside a repo, only `.claude/worktrees` and `.conductor` are walked (to find orphans git has forgotten). To scan specific folders instead, pass them: `npx worktreekill ~/code ~/work`.

## Keys

| Key        | Action |
|------------|--------|
| ↑ ↓ / j k  | Move |
| pgup pgdn  | Move a page |
| g G / home end | Jump to top or bottom |
| space      | Select |
| a          | Select all shown rows (press again to clear) |
| enter / d  | Delete selected (or the current row), after confirming |
| b          | Toggle also deleting the branch (`git branch -d`, which refuses unmerged branches) |
| s          | Sort: largest, oldest, repo |
| /          | Filter by path, branch, repo or tool |
| esc        | Clear the filter, or quit |
| q / ctrl+c | Quit (waits for deletions in progress) |

## How deletion works

- A worktree can't be deleted until its git status has loaded, so the prompt can warn about everything that would be lost: uncommitted changes, commits on no branch (detached HEAD), locks, and selected rows the filter hides.
- Normal worktrees are removed with `git worktree remove`. If one has uncommitted changes, `--force` is used only after you confirm, and only if the change count is still the same when deletion starts.
- Locked worktrees (Claude Code locks worktrees while an agent runs) get a warning. They are unlocked and then removed, so git still checks for uncommitted changes. If removal fails, the lock is put back.
- `dir missing`: the folder is already gone, so only git's entry for it is removed. If it is also locked (git's way of keeping worktrees on removable drives), it is left alone: run `git worktree unlock` first.
- `orphaned`: the repo is gone, so git can't remove it and the folder is deleted directly. Its contents can't be checked for uncommitted work. A worktree only counts as orphaned if the folder that held the repo still exists. When that is missing too (an unmounted drive, a devcontainer path), it isn't listed. Your home folder, the scanned folders and anything containing them are never deleted this way, and each orphan is checked again just before it is deleted.
- Branches are kept unless you turn on branch deletion with `b`.

## Scripting

```sh
npx worktreekill --list          # table (also used when output is piped)
npx worktreekill --json          # JSON, sorted by size
npx worktreekill --version
```

In JSON, `state` is `ok`, `missing` or `orphaned`.

## Security

worktreekill runs git in every worktree it finds, and git reads each repo's own config. A repo extracted from an archive (cloning never copies config) could use that config to make git run a program. worktreekill switches off the settings its git commands would act on:

- `core.fsmonitor`, which `git status` runs every time.
- Filters (`filter.<name>.clean` and friends) defined in the repo's own config, including files it includes. Filters from your global or system config, like git-lfs, keep working. A repo that sets up a filter only in its own config may show files as changed when they aren't.
- Signature checks in `git log` (`log.showSignature` with `gpg.program`).

Submodules have their own config, which isn't checked. This needs git 2.26 or newer; with older git, status fails and those worktrees can't be deleted from the tool.
