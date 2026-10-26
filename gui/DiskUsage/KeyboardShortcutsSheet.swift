import SwiftUI

/// A quick-reference sheet listing the app's keyboard shortcuts — the GUI's
/// equivalent of the TUI's '?' help overlay.
struct KeyboardShortcutsSheet: View {
    @Environment(\.dismiss) private var dismiss

    private let rows: [(String, String)] = [
        ("⌘ O", "Open a folder"),
        ("→", "Open the selected directory"),
        ("↩", "Open the selected entry"),
        ("← Esc", "Go up one level"),
        ("Space", "Mark / unmark for review"),
        ("⌘ R", "Rescan the directory on screen"),
        ("⌘ .", "Show/hide dot files"),
    ]

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Keyboard Shortcuts").font(.headline)
            ForEach(rows.indices, id: \.self) { i in
                let row = rows[i]
                HStack {
                    Text(row.0)
                        .font(.system(.body, design: .monospaced))
                        .frame(width: 60, alignment: .leading)
                    Text(row.1)
                        .foregroundStyle(.secondary)
                }
            }
            HStack {
                Spacer()
                Button("Done") { dismiss() }
                    .keyboardShortcut(.defaultAction)
            }
        }
        .padding(20)
        .frame(width: 340)
    }
}
