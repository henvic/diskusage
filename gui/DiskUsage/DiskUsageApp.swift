import SwiftUI

@main
struct DiskUsageApp: App {
    @StateObject private var viewModel = ExplorerViewModel()

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environmentObject(viewModel)
        }
        .commands {
            CommandGroup(replacing: .newItem) {
                Button("Open…") {
                    viewModel.presentOpenPanel()
                }
                .keyboardShortcut("o", modifiers: .command)
            }
            CommandMenu("Go") {
                Button("Back") {
                    viewModel.ascend()
                }
                .keyboardShortcut(.leftArrow, modifiers: .command)

                Button("Open Selected") {
                    viewModel.descendSelected()
                }
                .keyboardShortcut(.rightArrow, modifiers: .command)

                Button("Rescan") {
                    viewModel.rescanCurrent()
                }
                .keyboardShortcut("r", modifiers: .command)

                Divider()

                // Listed for discoverability only: the Space key itself is
                // handled by ExplorerViewModel's key monitor. A key equivalent
                // here would be unreliable, since SwiftUI rebuilds the menu
                // item as the selection changes.
                Button("Mark / Unmark for Review (Space)") {
                    viewModel.toggleMarkSelected()
                }
                .disabled(viewModel.selection == nil)

                Button(viewModel.hideDotfiles ? "Show Hidden Files" : "Hide Hidden Files") {
                    viewModel.hideDotfiles.toggle()
                }
                .keyboardShortcut(".", modifiers: .command)
            }
        }
    }
}
