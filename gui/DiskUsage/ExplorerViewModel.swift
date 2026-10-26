import AppKit
import SwiftUI

/// Owns the scanned tree, the current navigation path, scan settings, and
/// the session-only marks list — the GUI's equivalent of the TUI's explorer
/// state (see explorer in tui.go).
@MainActor
final class ExplorerViewModel: ObservableObject {
    @Published private(set) var root: Node? { didSet { refreshRows() } }
    /// Absolute paths from the root down to the directory on screen
    /// (inclusive); navigationPath[0] is always the root's path.
    @Published private(set) var navigationPath: [String] = [] { didSet { refreshRows() } }

    @Published var options = ScanOptions()
    @Published var measure: SizeMeasure = .onDisk { didSet { refreshRows() } }
    @Published var hideDotfiles = false { didSet { refreshRows() } }
    @Published var minSizeText = "" { didSet { refreshRows() } }

    /// The listing on screen — the current directory's children after the
    /// dot-file and minimum-size filters, sorted by the active measure — and
    /// the directory's total under that measure. Recomputed only when one of
    /// their inputs changes, never on a selection change, so moving through
    /// the list stays cheap.
    @Published private(set) var rows: [Entry] = []
    @Published private(set) var rowsTotal: Int64 = 0
    /// Bumped when rows that stay on screen change order relative to each
    /// other (switching measure, or a rescan that reshuffles sizes). The list
    /// is rebuilt instead of diffed then: a SwiftUI List that moves existing
    /// rows trips AppKit's "reentrant operation in its NSTableView delegate"
    /// warning, which Apple says will become an assert (reproduced with a
    /// bare List, none of this app's code involved).
    @Published private(set) var listGeneration = 0

    @Published private(set) var isScanning = false
    @Published private(set) var scannedCount: Int64 = 0
    @Published var lastError: DiskUsageProcessError?

    @Published private(set) var marks: [String] = [] { didSet { markedSet = Set(marks) } }
    @Published private(set) var markedSet: Set<String> = []
    @Published var showMarksPanel = false
    @Published var showOptionsSheet = false
    @Published var showShortcutsSheet = false

    @Published var selection: String?

    private var scanTask: Task<Void, Never>?
    private var rootRequestPath: String?
    private var inputMonitor: Any?

    var hasScanned: Bool { root != nil }

    // MARK: - Opening / scanning

    func presentOpenPanel() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.allowsMultipleSelection = false
        panel.message = "Choose a folder to scan"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        open(url: url)
    }

    func presentBinaryLocatorPanel() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = false
        panel.canChooseFiles = true
        panel.allowsMultipleSelection = false
        panel.message = "Choose the diskusage command-line executable"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        BinaryLocator.remember(url)
        lastError = nil
        if rootRequestPath != nil || root != nil {
            rescanFromRoot()
        }
    }

    func open(url: URL) {
        rootRequestPath = url.standardizedFileURL.path
        navigationPath = []
        rescanFromRoot()
    }

    /// Rescans from the original root, keeping the current breadcrumb where
    /// the fresh tree still has it.
    func rescanFromRoot() {
        guard let rootPath = rootRequestPath ?? root?.path else { return }
        let keepPath = navigationPath
        runScan(path: rootPath) { [weak self] fresh in
            guard let self else { return }
            self.root = fresh
            if keepPath.isEmpty {
                self.navigationPath = [fresh.path]
                return
            }
            var kept: [String] = [fresh.path]
            var node = fresh
            for p in keepPath.dropFirst() {
                guard let child = node.children?.first(where: { $0.path == p }) else { break }
                kept.append(p)
                node = child
            }
            self.navigationPath = kept
        }
    }

    /// Rescans the directory currently on screen from disk (the GUI's
    /// equivalent of the TUI's 'r'): a full refresh at the root, or just the
    /// open subtree deeper in.
    func rescanCurrent() {
        guard let root, let current = currentNode else { return }
        if current.path == root.path {
            rescanFromRoot()
            return
        }
        runScan(path: current.path) { [weak self] fresh in
            guard let self, let root = self.root else { return }
            self.root = root.replacingDescendant(at: fresh.path, with: fresh)
        }
    }

    private func runScan(path: String, apply: @escaping (Node) -> Void) {
        scanTask?.cancel()
        lastError = nil
        isScanning = true
        scannedCount = 0
        let opts = options
        scanTask = Task {
            do {
                let fresh = try await DiskUsageProcess.scan(path: path, options: opts) { [weak self] count in
                    Task { @MainActor in
                        self?.scannedCount = count
                    }
                }
                if Task.isCancelled { return }
                apply(fresh)
                self.isScanning = false
            } catch {
                if Task.isCancelled { return }
                self.lastError = (error as? DiskUsageProcessError) ?? .other(error.localizedDescription)
                self.isScanning = false
            }
        }
    }

    // MARK: - Navigation

    var currentNode: Node? {
        guard let root, let path = navigationPath.last else { return root }
        return root.find(path: path)
    }

    var breadcrumb: [Node] {
        guard let root else { return [] }
        var nodes = [root]
        var node = root
        for p in navigationPath.dropFirst() {
            guard let child = node.children?.first(where: { $0.path == p }) else { break }
            nodes.append(child)
            node = child
        }
        return nodes
    }

    /// Rebuilds `rows` and `rowsTotal` from the tree, then keeps a row
    /// selected — the one already selected if it is still listed, otherwise
    /// the first — so the arrow keys and Space always have a row to act on,
    /// like the TUI's cursor.
    private func refreshRows() {
        guard let current = currentNode else {
            rows = []
            rowsTotal = 0
            return
        }
        var kids = (current.children ?? []).map(Entry.init)
        if hideDotfiles {
            kids = kids.filter { !$0.name.hasPrefix(".") }
        }
        if let threshold = try? parseSize(minSizeText), threshold > 0 {
            kids = kids.filter { $0.bytes(measure) >= threshold }
        }
        let m = measure
        kids.sort { a, b in
            let (sa, sb) = (a.bytes(m), b.bytes(m))
            if sa != sb { return sa > sb }
            return a.name < b.name
        }
        if reordered(from: rows, to: kids) {
            listGeneration += 1
        }
        rows = kids
        rowsTotal = current.bytes(measure)
        if selection == nil || !kids.contains(where: { $0.id == selection }) {
            selection = kids.first?.id
        }
    }

    /// Whether rows present in both listings appear in a different order,
    /// ignoring rows that were added or removed.
    private func reordered(from old: [Entry], to new: [Entry]) -> Bool {
        let newIDs = Set(new.map(\.id))
        let oldIDs = Set(old.map(\.id))
        let kept = old.lazy.map(\.id).filter(newIDs.contains)
        let now = new.lazy.map(\.id).filter(oldIDs.contains)
        return !kept.elementsEqual(now)
    }

    func entry(at path: String?) -> Entry? {
        guard let path else { return nil }
        return rows.first { $0.id == path }
    }

    func descend(into path: String) {
        navigationPath.append(path)
        selection = rows.first?.id
    }

    /// Opens an entry the way Finder does on a double-click: a directory is
    /// browsed into, a file opens in its default app (the TUI's 'o'). Beeps
    /// when no app could open it, e.g. the file is gone since the scan.
    func openEntry(_ entry: Entry) {
        guard entry.isDir else {
            if !NSWorkspace.shared.open(URL(fileURLWithPath: entry.id)) {
                NSSound.beep()
            }
            return
        }
        descend(into: entry.id)
    }

    func descendSelected() {
        guard let entry = entry(at: selection), entry.isDir else { return }
        descend(into: entry.id)
    }

    /// Goes up one level with the directory just left selected, like the
    /// TUI, so ↑/↓ continue from where you were.
    func ascend() {
        guard navigationPath.count > 1 else { return }
        let left = navigationPath.removeLast()
        selection = left
    }

    func goToBreadcrumb(_ node: Node) {
        guard let idx = breadcrumb.firstIndex(where: { $0.path == node.path }),
              idx + 1 < navigationPath.count else { return }
        let left = navigationPath[idx + 1]
        navigationPath = Array(navigationPath.prefix(idx + 1))
        selection = left
    }

    // MARK: - Marks

    func toggleMark(_ path: String) {
        if let idx = marks.firstIndex(of: path) {
            marks.remove(at: idx)
        } else {
            marks.append(path)
        }
    }

    /// Installs the window's keyboard and click handling for the list, as a
    /// local event monitor so it sees events before the list does. The list
    /// is an NSTableView: it would take Space for its type-to-select search,
    /// it ignores ←/→, and its click tracking swallows any key pressed while
    /// the button is still down (see handleRowClick). Everything it does not
    /// handle passes through untouched.
    func installInputMonitor() {
        guard inputMonitor == nil else { return }
        inputMonitor = NSEvent.addLocalMonitorForEvents(matching: [.keyDown, .leftMouseDown]) { [weak self] event in
            guard let self else { return event }
            let handled = event.type == .leftMouseDown ? self.handleRowClick(event) : self.handleKey(event)
            return handled ? nil : event
        }
    }

    /// The explorer's keys, matching the TUI: Space marks or unmarks the
    /// selected row, → opens the selected directory, ← and Esc go up a level.
    /// They act only in the main window with no sheet up, while the list has
    /// the keyboard (so text fields and other controls keep their keys), and
    /// without modifiers (⌘← and ⌘→ are menu commands).
    private func handleKey(_ event: NSEvent) -> Bool {
        guard event.modifierFlags.intersection([.command, .control, .option, .shift]).isEmpty,
              let window = event.window,
              !(window is NSPanel), window.sheetParent == nil, window.attachedSheet == nil,
              window.firstResponder is NSTableView else { return false }
        switch event.keyCode {
        case 49: // Space
            guard selection != nil else { return false }
            toggleMarkSelected()
        case 124: // →
            descendSelected()
        case 123, 53: // ←, Esc
            ascend()
        default:
            return false
        }
        return true
    }

    /// Width of the leading part of a row, from the list's edge across the
    /// star, where a click toggles the mark instead of selecting the row.
    private static let starZone: CGFloat = 40

    /// Handles a plain left click on a row of the main window's list, and
    /// reports whether it did (the event is then dropped). NSTableView tracks
    /// the mouse until the button comes up, to spot drags, and drops every key
    /// pressed meanwhile — so a Space pressed just before the click was
    /// released was lost. Handling the click here means that tracking never
    /// starts: a click on the star toggles the mark, a second click on the
    /// selected row opens it, and any other click selects the row.
    /// Right-clicks and clicks with modifiers still go to the list.
    private func handleRowClick(_ event: NSEvent) -> Bool {
        guard event.modifierFlags.intersection([.command, .control, .option, .shift]).isEmpty,
              let window = event.window,
              !(window is NSPanel), window.sheetParent == nil, window.attachedSheet == nil,
              let hit = window.contentView?.hitTest(event.locationInWindow) else { return false }
        var view: NSView? = hit
        while let v = view, !(v is NSTableView) { view = v.superview }
        guard let table = view as? NSTableView else { return false }
        let point = table.convert(event.locationInWindow, from: nil)
        let row = table.row(at: point)
        guard row >= 0, row < rows.count else { return false }
        let entry = rows[row]
        window.makeFirstResponder(table)
        if point.x - table.rect(ofRow: row).minX < Self.starZone {
            toggleMark(entry.id)
        } else if event.clickCount >= 2 && selection == entry.id {
            openEntry(entry)
        } else {
            selection = entry.id
        }
        return true
    }

    /// Marks or unmarks the selected row (the Space key).
    func toggleMarkSelected() {
        guard let entry = entry(at: selection) else { return }
        toggleMark(entry.id)
    }

    func removeMark(_ path: String) {
        marks.removeAll { $0 == path }
    }

    func copyAllMarks() {
        let pasteboard = NSPasteboard.general
        pasteboard.clearContents()
        pasteboard.setString(marks.joined(separator: "\n"), forType: .string)
    }

    /// Navigates to the mark's location (selecting it inside its parent,
    /// without opening it), like the TUI's jumpTo.
    func jumpToMark(_ path: String) {
        guard let root, let chain = root.chain(to: path) else { return }
        let ancestors = chain.dropLast().map(\.path)
        navigationPath = ancestors.isEmpty ? [root.path] : Array(ancestors)
        selection = path
        showMarksPanel = false
    }

    // MARK: - Actions

    func copyPath(_ path: String) {
        let pasteboard = NSPasteboard.general
        pasteboard.clearContents()
        pasteboard.setString(path, forType: .string)
    }

    func revealInFinder(_ path: String) {
        NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: path)])
    }
}
