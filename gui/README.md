# DiskUsage for macOS

A native SwiftUI front end for `diskusage`. It shows the same drill-in/drill-out
explorer as the terminal UI, in a Mac window.

> [!WARNING]
> Like the rest of this repository, this app is mostly AI generated.

## Requirements

- macOS 13 (Ventura) or later
- Xcode 26 or later (the app icon is an Icon Composer document; Xcode also
  generates the flat icon older macOS versions use)
- Go (the version in `../go.mod`), with `go` on your `PATH` or installed in
  `/usr/local/go/bin`, `/opt/homebrew/bin`, or `/usr/local/bin`

## Build and run

```sh
open gui/DiskUsage.xcodeproj
```

Then press **Run** (⌘R). The first time, Xcode may ask you to pick a signing
team; "Sign to Run Locally" is enough.

## Using the app

Open a folder with ⌘O. The window shows, from top to bottom:

- **Header row:** the breadcrumb (click an earlier folder to jump back up),
  the **On disk / Apparent** switch with an ⓘ button explaining the two
  measures, and the **Show hidden files** checkbox.
- **Totals line:** the size of the folder on screen, with the other measure
  next to it when the two differ (for example "8.1 GiB on disk · 64.3 GiB
  apparent"), and its file and folder counts.
- **The list:** the folder's entries, largest first. Each row has a star, an
  icon, the name, a usage bar, its share of the folder, and its size.

Click the star on a row, or select it and press Space, to mark it **for
review**. The **For Review** toolbar button lists everything marked: **Show in
List** takes you to an entry, ✕ removes it, and **Copy All** copies every
path. Marks last until you quit.

### Keyboard

| Key | Action |
| --- | --- |
| ↑ / ↓ | Move the selection |
| → | Open the selected folder |
| ← or Esc | Go up one level, selecting the folder you came from |
| Return | Open the selected entry (a file opens in its default app) |
| Space | Mark / unmark the selected entry for review |
| ⌘O | Choose a folder to scan |
| ⌘R | Rescan the folder on screen |
| ⌘. | Show / hide dot files |
| ⌘← / ⌘→ | Go up / open the selected folder (Go menu) |

The arrow keys, Space and Esc act on the list only while it has the keyboard
focus, which it takes whenever the folder changes, so text fields keep their
keys.

## How it works

The app does not scan the disk itself. It runs the Go CLI as a subprocess and
reads its output:

```
diskusage --json --progress-json [-L] [-x] [--exclude …] <path>
```

- The **"Build diskusage CLI"** build phase (`Scripts/build-diskusage.sh`)
  compiles the Go module from the repository root and places the binary at
  `DiskUsage.app/Contents/Resources/diskusage`, so the app is self-contained.
- `--progress-json` writes `{"scanned":N}` lines to stderr. The app uses them
  for its "Scanning… N items" indicator.
- Every node in the JSON carries both sizes: on disk (`size`) and apparent
  (`apparent`). Switching between them only re-sorts, with no rescan. An
  older `diskusage` without the `apparent` fields still works; both measures
  then show the on-disk size.
- If the bundled binary is missing, the app looks on `PATH`, then in
  `/usr/local/bin`, `/opt/homebrew/bin`, and `~/go/bin`, then offers a
  "Locate diskusage…" file picker and remembers the path you choose.

The app is **not sandboxed**. A disk usage tool needs to read arbitrary
directories and to launch its helper binary, and the App Sandbox makes both
hard. This means the app can't be distributed through the Mac App Store as is.

### Source layout

| File | Role |
| --- | --- |
| `DiskUsageApp.swift` | App entry point and menu commands |
| `ContentView.swift` | Window: toolbar, breadcrumb, totals line, list rows, progress |
| `ExplorerViewModel.swift` | State: tree, navigation, the sorted listing, filters, marks, scanning; the list's keyboard and click handling |
| `DiskUsageProcess.swift` | Runs the CLI and decodes its JSON and progress |
| `BinaryLocator.swift` | Finds the `diskusage` executable |
| `Node.swift` | The JSON tree model and tree helpers, and `Entry`, the lightweight row the list draws |
| `ScanOptions.swift` | Scan settings and how they map to CLI flags |
| `Formatting.swift` | Ports of `humanize`, `comma`, and `parseSize` |
| `MarksPanel.swift` | The "Marked for Review" list |
| `MeasureHelp.swift` | The popover explaining on disk vs. apparent size |
| `MeterBar.swift`, `ScanOptionsSheet.swift`, `KeyboardShortcutsSheet.swift` | Supporting views |
| `AppIcon.icon` | App icon: the row meter's bars, largest first (edit in Icon Composer) |

## Features (parity with the TUI)

| TUI | GUI |
| --- | --- |
| ↑/↓, →/Enter, ←/Esc | Click or ↑/↓ to select; →, Return, or double-click to open; ← or Esc (or the breadcrumb) to go up. ⌘→/⌘← also work from the Go menu |
| `r` rescan | ⌘R or the toolbar (rescans only the directory on screen; at the root, rescans everything) |
| `.` dotfiles | ⌘. or the "Show hidden files" checkbox (a view filter only; totals don't change) |
| `m` / `M` marks | Star or Space marks for review; the "For Review" toolbar button opens the list (Show in List, remove, Copy All) |
| `c` copy path | Context menu "Copy Path" (absolute path only; there is no relative-path copy) |
| `o` open in default app | Return, double-click, or context menu "Open" on a file |
| `f` open folder in file manager | Context menu "Reveal in Finder" |
| `a` on disk / apparent | "On disk / Apparent" switch in the header row, applied instantly with no rescan; the totals line shows both sizes when they differ |
| `-L`, `-x`, `--exclude` | Options sheet (applying rescans from the root) |
| `--min` | Options sheet "Hide entries smaller than", applied instantly with no rescan |
| `⟳ scanning` counter | "Scanning… N items" indicator |
| `?` help | Keyboard shortcuts sheet |

## Known limitations

- **No browsing during a scan.** The TUI shows the tree as it fills in. The GUI
  gets one complete JSON tree when the subprocess exits, and shows only a
  progress counter until then.
- **Subtree rescans run in a new process.** The TUI reuses one scanner across
  rescans, so it remembers which hardlinks it has already counted and which
  device `-x` started on. The GUI starts a fresh CLI process for each rescan.
  Two results follow:
  - Hardlinks that cross the edge of the rescanned subtree may be counted again.
  - With `-x`, the device check is based on the subtree's own device.

  A full rescan at the root is always exact.
- **No "changed on disk" marker.** The TUI's `●` staleness check is not ported
  yet.
- **Row clicks are handled by the app, not the list.** The table behind the
  list drops any key pressed while a click is still held, which lost quick
  Space presses, so the app selects rows, toggles stars and opens on double
  click itself. A click counts as a star click within 40 points of the row's
  leading edge (`starZone` in `ExplorerViewModel.swift`); move that if the
  row layout changes.
- **Large trees use a lot of memory.** The whole tree is JSON-encoded, sent
  over a pipe, and decoded in memory. Scanning `/` works, but it is slower and
  uses more memory than the TUI.

## Roadmap

- Next: a treemap view next to the list.
