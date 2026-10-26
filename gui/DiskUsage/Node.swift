import Foundation

/// Which of a node's two sizes the explorer sorts and sizes by.
enum SizeMeasure: String, CaseIterable {
    case onDisk, apparent
}

/// One entry (file or directory) in the scanned tree — mirrors the JSON
/// `diskusage --json` emits (see Node in the Go package's scan.go). Every
/// node carries both sizes in bytes: the space allocated on disk (`size`,
/// `selfSize`, matching `du`) and the apparent, logical length (`apparent`,
/// `apparentSelf`, matching `ls -l`).
struct Node: Codable, Identifiable, Equatable {
    var name: String
    var path: String
    var isDir: Bool
    var size: Int64
    var selfSize: Int64
    var apparent: Int64
    var apparentSelf: Int64
    var files: Int64
    var dirs: Int64
    var children: [Node]?
    var error: String?

    enum CodingKeys: String, CodingKey {
        case name, path
        case isDir = "dir"
        case size
        case selfSize = "self"
        case apparent, apparentSelf
        case files, dirs, children, error
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        name = try c.decode(String.self, forKey: .name)
        path = try c.decode(String.self, forKey: .path)
        isDir = try c.decode(Bool.self, forKey: .isDir)
        size = try c.decode(Int64.self, forKey: .size)
        selfSize = try c.decode(Int64.self, forKey: .selfSize)
        // An older diskusage found on PATH predates the apparent fields; fall
        // back to the on-disk numbers so the app still works with it.
        apparent = try c.decodeIfPresent(Int64.self, forKey: .apparent) ?? size
        apparentSelf = try c.decodeIfPresent(Int64.self, forKey: .apparentSelf) ?? selfSize
        files = try c.decode(Int64.self, forKey: .files)
        dirs = try c.decode(Int64.self, forKey: .dirs)
        children = try c.decodeIfPresent([Node].self, forKey: .children)
        error = try c.decodeIfPresent(String.self, forKey: .error)
    }

    var id: String { path }

    var isUnreadable: Bool { error?.isEmpty == false }

    /// The node's total under the chosen measure.
    func bytes(_ measure: SizeMeasure) -> Int64 {
        measure == .apparent ? apparent : size
    }
}

/// One row of the explorer's listing: just the fields a row draws, copied
/// out of the tree. Rows are compared on every list update, and comparing two
/// Nodes walks their whole subtrees, so the list holds these instead.
struct Entry: Identifiable, Equatable {
    let id: String // the path
    let name: String
    let isDir: Bool
    let size: Int64
    let apparent: Int64
    let isUnreadable: Bool

    init(_ node: Node) {
        id = node.path
        name = node.name
        isDir = node.isDir
        size = node.size
        apparent = node.apparent
        isUnreadable = node.isUnreadable
    }

    func bytes(_ measure: SizeMeasure) -> Int64 {
        measure == .apparent ? apparent : size
    }
}

extension Node {
    /// Returns the chain of nodes from self down to the node at
    /// `targetPath` (inclusive), or nil if it isn't in this subtree.
    func chain(to targetPath: String) -> [Node]? {
        if path == targetPath { return [self] }
        guard let kids = children else { return nil }
        for k in kids {
            if targetPath == k.path || targetPath.hasPrefix(k.path + "/") {
                guard let rest = k.chain(to: targetPath) else { return nil }
                return [self] + rest
            }
        }
        return nil
    }

    /// Returns the node at `targetPath` within this subtree, or nil.
    func find(path targetPath: String) -> Node? {
        chain(to: targetPath)?.last
    }

    /// Returns a copy of self with the node at `targetPath` replaced by
    /// `replacement` — used after rescanning a subtree. Returns self
    /// unchanged if `targetPath` isn't found.
    func replacingDescendant(at targetPath: String, with replacement: Node) -> Node {
        if path == targetPath { return replacement }
        guard var kids = children else { return self }
        for i in kids.indices {
            if targetPath == kids[i].path || targetPath.hasPrefix(kids[i].path + "/") {
                kids[i] = kids[i].replacingDescendant(at: targetPath, with: replacement)
                var copy = self
                copy.children = kids
                return copy
            }
        }
        return self
    }
}
