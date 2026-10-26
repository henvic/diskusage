import SwiftUI

/// The explanation of the two size measures, shown in a popover from the ⓘ
/// button beside the "On disk / Apparent" control (see MeasureHelpButton in
/// ContentView.swift).
struct MeasureHelp: View {
    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Two ways to measure size").font(.headline)
            measure("On disk", "The space the files actually take up on the drive. What du and Finder's “on disk” report, and what you get back by deleting them.")
            measure("Apparent", "The length of the files' contents, as if written out byte by byte. What ls -l and the “bytes” figure in Get Info report, and the size of a copy or an archive.")
            Text("They differ for sparse files (VM and Docker images), iCloud files kept only in the cloud, compressed files, and folders full of tiny files that each occupy a whole block.")
                .foregroundStyle(.secondary)
        }
        .font(.callout)
        // Popover content is laid out at its ideal width unless told otherwise;
        // without this the lines run off the edge instead of wrapping.
        .fixedSize(horizontal: false, vertical: true)
        .frame(width: 360, alignment: .leading)
        .padding(16)
    }

    private func measure(_ name: String, _ text: String) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(name).fontWeight(.semibold)
            Text(text)
        }
    }
}
