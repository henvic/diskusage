# diskusage
[![Go Reference](https://pkg.go.dev/badge/github.com/henvic/diskusage.svg)](https://pkg.go.dev/github.com/henvic/diskusage) [![Build Status](https://github.com/henvic/diskusage/actions/workflows/go.yml/badge.svg)](https://github.com/henvic/diskusage/actions/workflows/go.yml) [![Coverage Status](https://coveralls.io/repos/henvic/diskusage/badge.svg)](https://coveralls.io/r/henvic/diskusage) [![Go Report Card](https://goreportcard.com/badge/github.com/henvic/diskusage)](https://goreportcard.com/report/github.com/henvic/diskusage)

A small, fast command-line tool that shows **where disk space goes** under a
path, so you can decide what to clean up. Think `du` with sorting, bars, and an
interactive explorer that opens by default — using only the Go standard library,
no dependencies.

[![diskusage exploring /Applications](https://github.com/user-attachments/assets/1631a56b-93fe-4ef4-a289-2f3b3fbffa9c)](https://asciinema.org/a/uqbcxgkfIQX2Hp1w)

> [!WARNING]
> Most of the code here is AI generated. It has been verified to work
> correctly, but it is not human handcrafted.

## Install

```sh
go install github.com/henvic/diskusage@latest
```

## Usage

```
diskusage [flags] [path]
```

With no path it scans the current directory. Every entry is measured both ways:
**space used on disk** (allocated blocks, matching `du`) and **apparent size**
(the logical length, matching `ls -l`). Listings sort by space on disk; pass
`--apparent` to sort by apparent size, or press `a` in the explorer to switch.
Totals show both numbers whenever they differ, which they do for sparse files
(VM and Docker images), compressed files, cloud-only placeholders, and
directories full of small files. On a
terminal it opens the interactive explorer by default; pass `--print` (or a
format flag), or pipe the output, to print instead. Flags may come before or
after the path; put `--` before a path that starts with `-`.

### Modes

| Command                         | What it does                                       |
| ------------------------------- | -------------------------------------------------- |
| `diskusage`                     | Interactive explorer — drill in and out (default)  |
| `diskusage --print`             | Largest items directly under the path, printed     |
| `diskusage --tree --depth 3`    | A size-sorted tree, three levels deep              |
| `diskusage --files --limit 20`  | The 20 biggest individual files in the subtree     |
| `diskusage --json`              | The full tree as JSON, for scripting               |

### Interactive keys

```
↑/↓ or j/k   move                →/Enter or l   open directory
←/h/Esc      go up (back)         g/G            first / last
c            copy absolute path   C              copy relative path
o            open in default app  f              open folder in file manager
r            rescan from disk     .              show/hide dotfiles
m            mark / unmark entry  M              marks panel
a            on disk / apparent   ?              show keys
q or Ctrl-C  quit
```

Esc backs out one level rather than quitting; at the root it does nothing, and
`q` or Ctrl-C exits. `?` shows the key reference. `.` toggles whether dot
("hidden") entries are listed — a view filter only, so sizes and totals never
change. `c` flashes the selected row
and copies its absolute path to the clipboard; `C` copies the path relative to
the current working directory. `o` opens the selected entry in its default app
(a directory opens in the file manager), and `f` opens the directory you are
viewing in the file manager. On macOS copy/open use `pbcopy` and `open`; on Linux
and other Unix systems (the BSDs, …) they fall back to `wl-copy`/`xclip`/`xsel`
and `xdg-open`. (Cmd+C can't be
bound: the terminal handles it itself to copy selected text, so it never
reaches the program.)

The window also redraws on resize.

**Marks (a staging area).** Press `m` to mark the highlighted entry (file or
directory) to check later — marked rows get a `*` and the footer shows the
count. `M` opens the marks panel, where you can review the list (shown as
absolute paths; press `a` to switch to paths relative to where you launched),
`m` to unmark, and Enter to jump straight to a mark's location. A mark whose
path is no longer in the scanned tree (deleted, then rescanned away) is dimmed
and tagged as gone. On quit, every marked path is printed to standard output as
an absolute path, one per line. Marks last for the session only.

Because the explorer draws on the terminal itself rather than on standard
output, `-i` can open it even when output is redirected, and the marks then go
wherever stdout points:

```sh
diskusage -i ~/Downloads > marks.txt   # explore, mark with m, quit
```

The explorer opens immediately, while the scan is still running: the listing
fills in and sizes grow in place, with a `⟳ scanning` counter in the header.
Sizes, percentages, and sort order are provisional until the counter
disappears; `r` (rescan) waits until then.

Once the scan completes, the tree is a snapshot, so edits you make on disk
afterwards are not shown until you press `r`. `r` rescans the directory you are
currently viewing — a full refresh at the root, or just the current subtree
deeper in — and updates the totals all the way up. The rescan runs in the
background with a `⟳ rescanning` counter in the header; you can keep browsing
the old numbers, and the fresh ones replace them when it finishes.

The explorer also watches the directory on screen and shows a `● changed on
disk` marker in the header when its contents change (a file added, removed, or
an immediate file growing). This check is shallow — it only covers the current
level, not deeper subdirectories — so treat it as a nudge to press `r`, not a
guarantee. It needs no external dependencies.

### Useful flags

- `-p`, `--print` — print output instead of opening the interactive explorer.
- `-i`, `--interactive` — open the explorer even when stdout is not a terminal.
- `--apparent` — sort and size by apparent (logical) size instead of space on
  disk.
- `--min 100M` — hide entries smaller than a size (`K`, `M`, `G`, `T`).
- `--limit N` — cap the number of entries shown.
- `--depth N` — depth limit for `--tree` (0 = unlimited).
- `--exclude '*.log'` — skip matching names/paths (repeatable).
- `-x` — stay on a single filesystem.
- `-L` — follow symbolic links (off by default; the link is counted, not its
  target). Symlink cycles and repeated targets are scanned only once.
- `--no-color` — plain output (also honors `NO_COLOR`).
- `--progress-json` — write scan progress to stderr as newline-delimited JSON
  (`{"scanned":N}`, ending with `"done":true`), even when stderr is not a
  terminal. Meant for tools that drive diskusage, such as the macOS app.
- `--version` — print the version and exit.

## Examples

```sh
diskusage ~/Downloads              # what's filling Downloads? (interactive)
diskusage /                        # explore the whole disk
diskusage --print ~/Downloads      # print the summary instead of exploring
diskusage --files --limit 30 ~     # the 30 biggest files under home
diskusage --min 1G --tree /var     # only the big stuff in /var, as a tree
diskusage --json . | jq '.children[0]'
```

## macOS app

[`gui/`](gui/) has a native SwiftUI version of the explorer, built on top of
this CLI. See [gui/README.md](gui/README.md).

https://github.com/user-attachments/assets/4c2b0ca4-3214-43c4-a597-4fd574d9759f

## Notes

- The whole tree is held in memory (like `ncdu`); on huge filesystems use `-x`,
  `--exclude`, or scan a narrower path.
- Hard-linked files are counted once, like `du`: whichever path the scan meets
  first carries the size, and the other links are listed at 0 B.
- Directories that cannot be read are flagged in the output — the sizes shown
  undercount whatever was unreadable.
- On Linux, kernel filesystems mounted below the scanned path (`/proc`, `/sys`,
  cgroup, debugfs, and similar) are skipped: they hold no data and report
  meaningless sizes. Naming one directly, as in `diskusage /proc`, still scans it.
- Control characters and invalid UTF-8 in file names are shown as escapes
  (`\n`, `\x1b`) so they cannot garble the screen. JSON output, copied paths,
  and the marks printed on quit keep the raw names.
- Disk-block accounting and `-x`/one-filesystem need Unix `stat`; on other
  platforms the on-disk size is the apparent size. The interactive explorer is
  Unix-only; elsewhere (and when output is piped without `-i`) diskusage prints
  the summary.
