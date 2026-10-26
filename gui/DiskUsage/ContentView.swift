import SwiftUI

/// The app's single window: a toolbar, a breadcrumb trail, a totals line,
/// and the sorted list of the current directory's children — the GUI's
/// equivalent of the TUI's drill-in/drill-out explorer (see explorer in
/// tui.go).
struct ContentView: View {
    @EnvironmentObject var viewModel: ExplorerViewModel

    var body: some View {
        Group {
            if viewModel.hasScanned {
                VStack(spacing: 0) {
                    HStack(spacing: 12) {
                        BreadcrumbView()
                        Picker("Size", selection: $viewModel.measure) {
                            Text("On disk").tag(SizeMeasure.onDisk)
                            Text("Apparent").tag(SizeMeasure.apparent)
                        }
                        .pickerStyle(.segmented)
                        .labelsHidden()
                        .fixedSize()
                        .help("Sort and size entries by the space they use on disk, or by their apparent (logical) size")
                        MeasureHelpButton()
                        Toggle("Show hidden files", isOn: Binding(
                            get: { !viewModel.hideDotfiles },
                            set: { viewModel.hideDotfiles = !$0 }
                        ))
                        .toggleStyle(.checkbox)
                        .help("List entries whose names start with a dot (⌘.)")
                    }
                    .padding(.horizontal, 12)
                    .padding(.top, 10)
                    HeaderSummaryView()
                        .padding(.horizontal, 12)
                        .padding(.bottom, 8)
                    Divider()
                    ExplorerListView()
                }
            } else {
                EmptyStateView()
            }
        }
        .frame(minWidth: 720, minHeight: 460)
        .onAppear { viewModel.installInputMonitor() }
        .toolbar {
            ToolbarItemGroup {
                Button {
                    viewModel.presentOpenPanel()
                } label: {
                    Label("Open", systemImage: "folder")
                }
                .help("Choose a folder to scan")

                Button {
                    viewModel.ascend()
                } label: {
                    Label("Back", systemImage: "chevron.left")
                }
                .disabled(viewModel.navigationPath.count <= 1)
                .help("Go up one level")

                Button {
                    viewModel.rescanCurrent()
                } label: {
                    Label("Rescan", systemImage: "arrow.clockwise")
                }
                .disabled(!viewModel.hasScanned || viewModel.isScanning)
                .help("Rescan the directory on screen")

                Button {
                    viewModel.showMarksPanel = true
                } label: {
                    Label("For Review (\(viewModel.marks.count))", systemImage: "star")
                }
                .help("Show the entries marked for review")

                Button {
                    viewModel.showOptionsSheet = true
                } label: {
                    Label("Options", systemImage: "slider.horizontal.3")
                }
                .help("Scan options")

                Button {
                    viewModel.showShortcutsSheet = true
                } label: {
                    Label("Shortcuts", systemImage: "keyboard")
                }
                .help("Keyboard shortcuts")
            }
        }
        .overlay(alignment: .bottom) {
            if viewModel.isScanning {
                ScanProgressOverlay()
                    .padding(.bottom, 12)
            }
        }
        .sheet(isPresented: $viewModel.showMarksPanel) { MarksPanel() }
        .sheet(isPresented: $viewModel.showOptionsSheet) { ScanOptionsSheet() }
        .sheet(isPresented: $viewModel.showShortcutsSheet) { KeyboardShortcutsSheet() }
        .alert(
            "Couldn't scan",
            isPresented: Binding(
                get: { viewModel.lastError != nil },
                set: { if !$0 { viewModel.lastError = nil } }
            ),
            presenting: viewModel.lastError
        ) { err in
            Button("OK") { viewModel.lastError = nil }
            if err == .binaryNotFound {
                Button("Locate diskusage…") { viewModel.presentBinaryLocatorPanel() }
            }
        } message: { err in
            Text(err.errorDescription ?? "Unknown error")
        }
    }
}

/// The breadcrumb trail above the list: the root's full path, then each
/// directory name down to the one on screen. Clicking an earlier entry
/// jumps back up to it.
private struct BreadcrumbView: View {
    @EnvironmentObject var viewModel: ExplorerViewModel

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 4) {
                let crumbs = viewModel.breadcrumb
                ForEach(Array(crumbs.enumerated()), id: \.element.id) { index, node in
                    Button(index == 0 ? node.path : node.name) {
                        viewModel.goToBreadcrumb(node)
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(index == crumbs.count - 1 ? Color.primary : Color.accentColor)
                    .fontWeight(index == crumbs.count - 1 ? .semibold : .regular)

                    if index < crumbs.count - 1 {
                        Image(systemName: "chevron.right")
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                    }
                }
            }
        }
        .font(.system(size: 13))
    }
}

/// A small ⓘ button beside the size-measure control that opens a popover
/// explaining the two measures, for anyone who has not met "apparent size"
/// before.
private struct MeasureHelpButton: View {
    @State private var showing = false

    var body: some View {
        Button {
            showing.toggle()
        } label: {
            Image(systemName: "info.circle")
                .foregroundStyle(.secondary)
        }
        .buttonStyle(.plain)
        .help("What do these sizes mean?")
        .popover(isPresented: $showing, arrowEdge: .bottom) {
            MeasureHelp()
        }
    }
}

/// The totals line under the breadcrumb: the size under the active measure
/// (and the other one too, when they differ — see totals in the Go
/// package's report.go), file/dir counts, and an unreadable flag for the
/// directory on screen.
private struct HeaderSummaryView: View {
    @EnvironmentObject var viewModel: ExplorerViewModel

    var body: some View {
        if let current = viewModel.currentNode {
            let disk = humanizeBytes(current.size)
            let apparent = humanizeBytes(current.apparent)
            let byApparent = viewModel.measure == .apparent
            HStack(spacing: 10) {
                Text(byApparent ? apparent : disk).fontWeight(.semibold)
                if disk != apparent {
                    Text(byApparent ? "apparent · \(disk) on disk" : "on disk · \(apparent) apparent")
                }
                Text("·")
                Text("\(commaFormat(current.files)) files")
                Text("·")
                Text("\(commaFormat(current.dirs)) dirs")
                if current.isUnreadable {
                    Text("· unreadable").foregroundStyle(.red)
                }
                Spacer()
            }
            .font(.callout)
            .foregroundStyle(.secondary)
        }
    }
}

/// Shown before any folder has been scanned.
private struct EmptyStateView: View {
    @EnvironmentObject var viewModel: ExplorerViewModel

    var body: some View {
        VStack(spacing: 16) {
            Image(systemName: "internaldrive")
                .font(.system(size: 48))
                .foregroundStyle(.secondary)
            Text("Pick a folder to see where its space goes.")
                .font(.title3)
            Button("Choose Folder…") {
                viewModel.presentOpenPanel()
            }
            .keyboardShortcut(.defaultAction)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

/// The sorted list of the current directory's children. It takes keyboard
/// focus whenever the directory on screen changes (and when it first
/// appears), so ↑/↓ and Space work without clicking a row first.
private struct ExplorerListView: View {
    @EnvironmentObject var viewModel: ExplorerViewModel
    @FocusState private var focused: Bool

    var body: some View {
        let total = viewModel.rowsTotal
        let measure = viewModel.measure
        let marked = viewModel.markedSet
        List(viewModel.rows, selection: $viewModel.selection) { entry in
            RowView(entry: entry, total: total, measure: measure, isMarked: marked.contains(entry.id))
                .equatable()
        }
        .listStyle(.plain)
        // The list's context menu, and its primary action for Return. Plain
        // left clicks never reach the list: ExplorerViewModel.handleRowClick
        // takes them (select, star, double-click to open), because the
        // table's own click tracking swallows a Space pressed mid-click.
        .contextMenu(forSelectionType: String.self) { ids in
            if let entry = viewModel.entry(at: ids.first) {
                Button("Open") { viewModel.openEntry(entry) }
                Divider()
                Button("Copy Path") { viewModel.copyPath(entry.id) }
                Button("Reveal in Finder") { viewModel.revealInFinder(entry.id) }
            }
        } primaryAction: { ids in
            if let entry = viewModel.entry(at: ids.first) {
                viewModel.openEntry(entry)
            }
        }
        .focused($focused)
        .onAppear { focused = true }
        .onChange(of: viewModel.navigationPath) { _ in focused = true }
        .overlay {
            if viewModel.rows.isEmpty {
                Text(viewModel.isScanning ? "Scanning…" : "(empty)")
                    .foregroundStyle(.secondary)
            }
        }
    }
}

/// One row: a star that marks the entry for review (filled when marked), a
/// folder/file icon, name, size under the active measure, percentage of the
/// current directory's total, and a usage bar — the GUI's equivalent of the
/// TUI's row (see rowSpans in tui.go). Clicks on the star are handled by
/// ExplorerViewModel.handleRowClick, which calls the same toggle. It takes
/// plain values and is
/// Equatable, so SwiftUI skips re-rendering a
/// row unless what it shows changed — moving the selection redraws nothing.
private struct RowView: View, Equatable {
    let entry: Entry
    let total: Int64
    let measure: SizeMeasure
    let isMarked: Bool

    static func == (a: RowView, b: RowView) -> Bool {
        a.entry == b.entry && a.total == b.total && a.measure == b.measure && a.isMarked == b.isMarked
    }

    private var size: Int64 { entry.bytes(measure) }

    private var fraction: Double {
        total > 0 ? Double(size) / Double(total) : 0
    }

    var body: some View {
        HStack(spacing: 8) {
            Image(systemName: isMarked ? "star.fill" : "star")
                .foregroundStyle(isMarked ? Color.yellow : Color.secondary.opacity(0.4))
                .frame(width: 18)
                .help(isMarked ? "Remove from review (Space)" : "Mark for review (Space)")

            Image(systemName: entry.isDir ? "folder.fill" : "doc")
                .foregroundStyle(entry.isDir ? Color.accentColor : Color.secondary)
                .frame(width: 18)

            Text(entry.name)
                .fontWeight(entry.isDir ? .medium : .regular)
                .lineLimit(1)
                .truncationMode(.middle)

            if entry.isUnreadable {
                Text("unreadable")
                    .font(.caption2)
                    .foregroundStyle(.red)
            }

            Spacer(minLength: 8)

            MeterBar(fraction: fraction)
                .frame(width: 80)

            Text(String(format: "%.0f%%", fraction * 100))
                .font(.caption)
                .foregroundStyle(.secondary)
                .frame(width: 36, alignment: .trailing)

            Text(humanizeBytes(size))
                .font(.system(.body, design: .monospaced))
                .frame(width: 84, alignment: .trailing)
        }
        .padding(.vertical, 2)
    }
}

/// The "scanning… N items" indicator shown while a scan or rescan runs.
private struct ScanProgressOverlay: View {
    @EnvironmentObject var viewModel: ExplorerViewModel

    var body: some View {
        HStack(spacing: 8) {
            ProgressView()
                .controlSize(.small)
            Text("Scanning… \(commaFormat(viewModel.scannedCount)) items")
                .font(.callout)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 8)
        .background(.regularMaterial, in: Capsule())
        .shadow(radius: 4)
    }
}
