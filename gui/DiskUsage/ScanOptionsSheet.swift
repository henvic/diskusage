import SwiftUI

/// The scan-options sheet: toggles and fields that change how diskusage
/// scans (follow symlinks, stay on one filesystem, exclude patterns), plus a
/// view-only minimum-size filter.
/// Applying re-scans from the root when a scan-affecting option changed.
struct ScanOptionsSheet: View {
    @EnvironmentObject var viewModel: ExplorerViewModel
    @Environment(\.dismiss) private var dismiss

    @State private var excludesText: String = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Scan Options").font(.headline)

            Toggle("Follow symbolic links", isOn: $viewModel.options.follow)
            Toggle("Stay on one filesystem (-x)", isOn: $viewModel.options.oneFilesystem)

            VStack(alignment: .leading, spacing: 4) {
                Text("Exclude patterns (one glob per line, e.g. *.log)")
                    .font(.subheadline)
                TextEditor(text: $excludesText)
                    .font(.system(.body, design: .monospaced))
                    .frame(height: 80)
                    .overlay(
                        RoundedRectangle(cornerRadius: 4)
                            .stroke(Color.secondary.opacity(0.3))
                    )
            }

            VStack(alignment: .leading, spacing: 4) {
                Text("Hide entries smaller than (view filter only, e.g. 100M)")
                    .font(.subheadline)
                TextField("e.g. 100M", text: $viewModel.minSizeText)
                    .textFieldStyle(.roundedBorder)
                    .frame(width: 160)
            }

            Spacer()

            HStack {
                Spacer()
                Button("Cancel") { dismiss() }
                Button("Apply") {
                    viewModel.options.excludes = excludesText
                        .split(separator: "\n")
                        .map { $0.trimmingCharacters(in: .whitespaces) }
                        .filter { !$0.isEmpty }
                    viewModel.rescanFromRoot()
                    dismiss()
                }
                .keyboardShortcut(.defaultAction)
            }
        }
        .padding(20)
        .frame(width: 420, height: 410)
        .onAppear {
            excludesText = viewModel.options.excludes.joined(separator: "\n")
        }
    }
}
