import SwiftUI

/// A compact horizontal usage meter: a track with a filled portion
/// proportional to `fraction` (0...1) — the GUI's equivalent of the TUI's
/// ASCII bar (see bar in report.go).
struct MeterBar: View {
    var fraction: Double
    var tint: Color = .accentColor

    private var clamped: Double { min(1, max(0, fraction)) }

    var body: some View {
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                RoundedRectangle(cornerRadius: 2)
                    .fill(Color.secondary.opacity(0.15))
                RoundedRectangle(cornerRadius: 2)
                    .fill(tint)
                    .frame(width: geo.size.width * clamped)
            }
        }
        .frame(height: 6)
    }
}
