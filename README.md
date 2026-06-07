# diskusage

A small, fast command-line tool that shows **where disk space goes** under a
path, so you can decide what to clean up. Think `du` with sorting, bars, and an
interactive explorer — using only the Go standard library, no dependencies.

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
disk blocks** (matching `du`); pass `--apparent` for logical file sizes.

### Modes

| Command                         | What it does                                       |
| ------------------------------- | -------------------------------------------------- |
| `diskusage`                     | Largest items directly under the path (default)    |
| `diskusage -i`                  | Interactive explorer — drill in and out            |
| `diskusage --tree --depth 3`    | A size-sorted tree, three levels deep              |
| `diskusage --files --limit 20`  | The 20 biggest individual files in the subtree     |
| `diskusage --json`              | The full tree as JSON, for scripting               |

### Interactive keys (`-i`)

```
↑/↓ or j/k   move          →/Enter or l   open directory
←/h/Esc      go up (back)   r              rescan from disk
g/G          first / last   q or Ctrl-C    quit
```

Esc backs out one level rather than quitting; at the root it does nothing. Use `q` or Ctrl-C to exit.

The tree is a snapshot taken when the scan ran, so edits you make on disk
afterwards are not shown until you press `r`. `r` rescans the directory you are
currently viewing — a full refresh at the root, or just the current subtree
deeper in — and updates the totals all the way up.

### Useful flags

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
diskusage ~/Downloads              # what's filling Downloads?
diskusage -i /                     # explore the whole disk
diskusage --files --limit 30 ~     # the 30 biggest files under home
diskusage --min 1G --tree /var     # only the big stuff in /var, as a tree
diskusage --json . | jq '.children[0]'
```

## Notes

- The whole tree is held in memory (like `ncdu`); on huge filesystems use `-x`,
  `--exclude`, or scan a narrower path.
- Disk-block accounting and `-x`/one-filesystem need Unix `stat`; on other
  platforms it falls back to apparent size. The interactive explorer is
  Unix-only.
