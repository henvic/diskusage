import Foundation

/// IEC byte formatting (KiB, MiB, ...), mirroring the CLI's `humanize` in
/// report.go so numbers read the same across the TUI, CLI, and GUI.
func humanizeBytes(_ b: Int64) -> String {
    let unit: Int64 = 1024
    if b < unit { return "\(b) B" }
    let units = ["KiB", "MiB", "GiB", "TiB", "PiB", "EiB"]
    var div = unit
    var exp = 0
    var n = b / unit
    while n >= unit && exp < units.count - 1 {
        div *= unit
        exp += 1
        n /= unit
    }
    var v = Double(b) / Double(div)
    // Just under a boundary, "%.1f" could round up to the next unit's
    // "1024.0"; promote the unit instead so it reads "1.0" of the next one.
    if (v * 10).rounded() / 10 >= Double(unit), exp < units.count - 1 {
        v /= Double(unit)
        exp += 1
    }
    return String(format: "%.1f %@", v, units[exp])
}

private let groupingFormatter: NumberFormatter = {
    let f = NumberFormatter()
    f.numberStyle = .decimal
    f.usesGroupingSeparator = true
    f.groupingSeparator = ","
    return f
}()

/// Groups an integer with thousands separators, mirroring the CLI's `comma`.
func commaFormat(_ n: Int64) -> String {
    groupingFormatter.string(from: NSNumber(value: n)) ?? String(n)
}

enum SizeParseError: Error {
    case invalid
}

/// Parses a human size like "10M" or "1.5GiB" into bytes, mirroring the
/// CLI's parseSize. An empty string is zero. Used only for the GUI's
/// client-side "hide entries smaller than" view filter.
func parseSize(_ raw: String) throws -> Int64 {
    let s = raw.trimmingCharacters(in: .whitespaces)
    if s.isEmpty { return 0 }
    var numEnd = s.startIndex
    while numEnd < s.endIndex, s[numEnd].isNumber || s[numEnd] == "." {
        numEnd = s.index(after: numEnd)
    }
    let numPart = String(s[s.startIndex..<numEnd])
    let unitPart = String(s[numEnd...]).trimmingCharacters(in: .whitespaces).lowercased()
    let mult: Double
    switch unitPart {
    case "", "b": mult = 1
    case "k", "kb", "kib": mult = Double(Int64(1) << 10)
    case "m", "mb", "mib": mult = Double(Int64(1) << 20)
    case "g", "gb", "gib": mult = Double(Int64(1) << 30)
    case "t", "tb", "tib": mult = Double(Int64(1) << 40)
    case "p", "pb", "pib": mult = Double(Int64(1) << 50)
    default: throw SizeParseError.invalid
    }
    guard let value = Double(numPart) else { throw SizeParseError.invalid }
    return Int64(value * mult)
}
