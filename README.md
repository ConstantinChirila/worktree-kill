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
| SIZE      | Disk usage (hard-linked files, e.g. from pnpm, are counted once) |
| LAST USED | Newest of the last commit and the last file change |
| STATUS    | `clean`, `N changed` (uncommitted), `N unpushed` (commits on neither the main branch nor any remote), `locked`, `dir missing`, `orphaned` |

Results appear as they're found. Size and status fill in as they're calculated.

## Where it looks

With no arguments it scans your home folder, skipping `node_modules`, `~/Library` and hidden folders. It also scans the places tools keep worktrees:

- `~/.claude-worktrees`, `<repo>/.claude/worktrees` (Claude Code)
- `~/.cursor/worktrees` (Cursor)
- `~/.codex/worktrees`, `$CODEX_HOME/worktrees` (Codex)
- `~/conductor`, `<repo>/.conductor` (Conductor)
- `~/.t3/worktrees` (T3 Code)

Worktrees registered in any repo it finds are listed wherever they live on disk. To scan specific folders instead, pass them: `npx worktreekill ~/code ~/work`.

## Keys

| Key        | Action |
|------------|--------|
| ↑ ↓ / j k  | Move |
| space      | Select |
| a          | Select all |
| enter / d  | Delete selected (or the current row), after confirming |
| b          | Toggle also deleting the branch (`git branch -d`, which refuses unmerged branches) |
| s          | Sort: largest, oldest, repo |
| /          | Filter by path, branch, repo or tool |
| q / esc    | Quit |

## How deletion works

- Normal worktrees are removed with `git worktree remove`. If a worktree has uncommitted changes, the confirm prompt says so and `--force` is used only after you confirm.
- Locked worktrees (Claude Code locks worktrees while an agent runs) get a warning and need a double `--force`.
- `dir missing`: the folder is already gone, so git's stale entry is cleaned up with `git worktree prune`.
- `orphaned`: the repo the worktree belonged to is gone, so git can't remove it. The folder is deleted directly.
- Branches are kept unless you turn on branch deletion with `b`.

## Scripting

```sh
npx worktreekill --list          # table
npx worktreekill --json          # JSON, sorted by size
```
