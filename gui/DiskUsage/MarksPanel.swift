import SwiftUI

/// The marks panel: the session's staged paths (the GUI's equivalent of the
/// TUI's 'M' overlay), with jump-to, unmark, and copy-all actions. Marks are
/// session-only, same as the TUI.
struct MarksPanel: View {
    @EnvironmentObject var viewModel: ExplorerViewModel
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Text("Marked for Review").font(.headline)
                Spacer()
                Button("Copy All") { viewModel.copyAllMarks() }
                    .disabled(viewModel.marks.isEmpty)
                Button("Done") { dismiss() }
            }
            .padding()

            Divider()

            if viewModel.marks.isEmpty {
                Text("Nothing marked yet. Click the star on a row, or select it and press Space.")
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                List {
                    ForEach(viewModel.marks, id: \.self) { path in
                        MarkRow(path: path)
                    }
                }
            }
        }
        .frame(width: 480, height: 360)
    }
}

/// One row in the marks panel: the entry's name and full path, a "gone" tag
/// if it's no longer in the scanned tree, and jump/unmark actions.
private struct MarkRow: View {
    @EnvironmentObject var viewModel: ExplorerViewModel
    let path: String

    private var isGone: Bool {
        viewModel.root?.find(path: path) == nil
    }

    var body: some View {
        HStack {
            VStack(alignment: .leading, spacing: 2) {
                Text((path as NSString).lastPathComponent)
                    .fontWeight(.medium)
                Text(path)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
            Spacer()
            if isGone {
                Text("gone")
                    .font(.caption2)
                    .foregroundStyle(.red)
            } else {
                Button("Show in List") { viewModel.jumpToMark(path) }
                    .buttonStyle(.link)
            }
            Button {
                viewModel.removeMark(path)
            } label: {
                Image(systemName: "xmark.circle")
            }
            .buttonStyle(.plain)
            .help("Remove from review")
        }
    }
}
