import Foundation

/// Locates the `diskusage` CLI executable the GUI shells out to.
enum BinaryLocator {
    private static let rememberedPathKey = "DiskUsageCLIPath"

    /// Finds `diskusage`: first bundled inside the app (where this Xcode
    /// project's "Build diskusage CLI" script places it), then common
    /// install locations and PATH, then a location the user previously
    /// picked by hand via "Locate diskusage…".
    static func locate() throws -> URL {
        if let bundled = Bundle.main.url(forResource: "diskusage", withExtension: nil),
           FileManager.default.isExecutableFile(atPath: bundled.path) {
            return bundled
        }
        for dir in searchDirectories() {
            let url = URL(fileURLWithPath: dir).appendingPathComponent("diskusage")
            if FileManager.default.isExecutableFile(atPath: url.path) {
                return url
            }
        }
        if let remembered = UserDefaults.standard.string(forKey: rememberedPathKey),
           FileManager.default.isExecutableFile(atPath: remembered) {
            return URL(fileURLWithPath: remembered)
        }
        throw DiskUsageProcessError.binaryNotFound
    }

    /// Remembers a path the user picked by hand, so future launches don't
    /// need to ask again.
    static func remember(_ url: URL) {
        UserDefaults.standard.set(url.path, forKey: rememberedPathKey)
    }

    private static func searchDirectories() -> [String] {
        var dirs: [String] = []
        if let pathVar = ProcessInfo.processInfo.environment["PATH"] {
            dirs.append(contentsOf: pathVar.split(separator: ":").map(String.init))
        }
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        dirs.append(contentsOf: ["/usr/local/bin", "/opt/homebrew/bin", "\(home)/go/bin"])
        return dirs
    }
}
