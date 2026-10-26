import Foundation

/// Errors that can occur while running the `diskusage` CLI as a subprocess.
enum DiskUsageProcessError: LocalizedError, Equatable {
    case binaryNotFound
    case launchFailed(String)
    case nonZeroExit(status: Int32, detail: String)
    case decodingFailed(String)
    case other(String)

    var errorDescription: String? {
        switch self {
        case .binaryNotFound:
            return "Couldn't find the diskusage command-line tool.\n\nBuild this Xcode project once — its \"Build diskusage CLI\" script compiles it automatically — or install Go and the diskusage CLI yourself and make sure it's on your PATH."
        case .launchFailed(let reason):
            return "Couldn't start diskusage: \(reason)"
        case .nonZeroExit(let status, let detail):
            let trimmed = detail.trimmingCharacters(in: .whitespacesAndNewlines)
            return trimmed.isEmpty ? "diskusage exited with status \(status)." : trimmed
        case .decodingFailed(let reason):
            return "Couldn't parse diskusage's output: \(reason)"
        case .other(let reason):
            return reason
        }
    }
}

/// Runs the `diskusage` CLI as a subprocess, decodes its JSON tree, and
/// reports scan progress by watching its stderr for the newline-delimited
/// JSON lines `--progress-json` emits ({"scanned":N}, finishing with
/// {"scanned":N,"done":true}).
enum DiskUsageProcess {
    private struct ProgressEvent: Decodable {
        var scanned: Int64
        var done: Bool?
    }

    static func scan(
        path: String,
        options: ScanOptions,
        onProgress: @escaping (Int64) -> Void
    ) async throws -> Node {
        let binary = try BinaryLocator.locate()

        let process = Process()
        process.executableURL = binary
        process.arguments = options.arguments(forPath: path)

        let stdout = Pipe()
        let stderr = Pipe()
        process.standardOutput = stdout
        process.standardError = stderr

        do {
            try process.run()
        } catch {
            throw DiskUsageProcessError.launchFailed(error.localizedDescription)
        }

        return try await withTaskCancellationHandler(
            operation: { () async throws -> Node in
                // Drain stderr for progress concurrently with reading stdout —
                // both pipes must be read as the child writes, or a full pipe
                // buffer could block the child (and so deadlock waitUntilExit).
                let stderrTask = Task.detached { () async throws -> String in
                    var otherLines: [String] = []
                    for try await line in stderr.fileHandleForReading.bytes.lines {
                        if let data = line.data(using: .utf8),
                           let event = try? JSONDecoder().decode(ProgressEvent.self, from: data) {
                            onProgress(event.scanned)
                        } else if !line.isEmpty {
                            otherLines.append(line)
                        }
                    }
                    return otherLines.joined(separator: "\n")
                }

                let stdoutData = try await Task.detached {
                    try stdout.fileHandleForReading.readToEnd() ?? Data()
                }.value

                await Task.detached { process.waitUntilExit() }.value
                let stderrDetail = (try? await stderrTask.value) ?? ""

                if process.terminationStatus != 0 {
                    throw DiskUsageProcessError.nonZeroExit(status: process.terminationStatus, detail: stderrDetail)
                }

                do {
                    return try JSONDecoder().decode(Node.self, from: stdoutData)
                } catch {
                    throw DiskUsageProcessError.decodingFailed(error.localizedDescription)
                }
            },
            onCancel: {
                if process.isRunning {
                    process.terminate()
                }
            }
        )
    }
}
