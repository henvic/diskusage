import Foundation

/// The scan settings that affect what diskusage actually walks — changing
/// any of these requires a fresh scan (unlike the size measure, dot-file
/// and minimum-size filters, which are applied client-side in
/// ExplorerViewModel).
struct ScanOptions: Equatable {
    var follow = false
    var oneFilesystem = false
    var excludes: [String] = []

    /// Builds the diskusage CLI arguments for scanning `path` with these
    /// options, always in JSON mode with machine-readable progress.
    func arguments(forPath path: String) -> [String] {
        var args = ["--json", "--progress-json"]
        if follow { args.append("-L") }
        if oneFilesystem { args.append("-x") }
        for pattern in excludes {
            args.append("--exclude")
            args.append(pattern)
        }
        args.append(path)
        return args
    }
}
