# diskusage

A small, fast command-line tool that shows **where disk space goes** under a
path, so you can decide what to clean up. Think `du` with sorting, bars, and an
interactive explorer that opens by default — using only the Go standard library,
no dependencies.

## Build

```sh
go build -o diskusage
```

Or run without installing:

```sh
go run . [flags] [path]
```

## Usage

```
diskusage [flags] [path]
```

With no path it scans the current directory. Size is measured in **allocated
disk blocks** (matching `du`); pass `--apparent` for logical file sizes. On a
terminal it opens the interactive explorer by default; pass `--print` (or a
format flag), or pipe the output, to print instead.

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
f            open in Finder       r              rescan from disk
?            show keys            q or Ctrl-C    quit
```

Esc backs out one level rather than quitting; at the root it does nothing, and
`q` or Ctrl-C exits. `?` shows the key reference. `c` flashes the selected row
and copies its absolute path to the clipboard; `C` copies the path relative to
the current working directory. `f` opens the directory you are viewing in the
file manager. On macOS copy/open use `pbcopy` and `open` (Finder); on Linux
they fall back to `wl-copy`/`xclip`/`xsel` and `xdg-open`. (Cmd+C can't be
bound: the terminal handles it itself to copy selected text, so it never
reaches the program.)

The window also redraws on resize.

The tree is a snapshot taken when the scan ran, so edits you make on disk
afterwards are not shown until you press `r`. `r` rescans the directory you are
currently viewing — a full refresh at the root, or just the current subtree
deeper in — and updates the totals all the way up.

The explorer also watches the directory on screen and shows a `● changed on
disk` marker in the header when its contents change (a file added, removed, or
an immediate file growing). This check is shallow — it only covers the current
level, not deeper subdirectories — so treat it as a nudge to press `r`, not a
guarantee. It needs no external dependencies.

### Useful flags

- `-p`, `--print` — print output instead of opening the interactive explorer.
- `--apparent` — logical file size instead of disk blocks.
- `--min 100M` — hide entries smaller than a size (`K`, `M`, `G`, `T`).
- `--limit N` — cap the number of entries shown.
- `--depth N` — depth limit for `--tree` (0 = unlimited).
- `--exclude '*.log'` — skip matching names/paths (repeatable).
- `-x` — stay on a single filesystem.
- `-L` — follow symbolic links (off by default; the link is counted, not its target).
- `--no-color` — plain output (also honors `NO_COLOR`).

## Examples

```sh
diskusage ~/Downloads              # what's filling Downloads? (interactive)
diskusage /                        # explore the whole disk
diskusage --print ~/Downloads      # print the summary instead of exploring
diskusage --files --limit 30 ~     # the 30 biggest files under home
diskusage --min 1G --tree /var     # only the big stuff in /var, as a tree
diskusage --json . | jq '.children[0]'
```

## Notes

- The whole tree is held in memory (like `ncdu`); on huge filesystems use `-x`,
  `--exclude`, or scan a narrower path.
- Disk-block accounting and `-x`/one-filesystem need Unix `stat`; on other
  platforms it falls back to apparent size. The interactive explorer is
  Unix-only; elsewhere (and when output is piped) diskusage prints the summary.
