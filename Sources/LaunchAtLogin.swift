import Foundation
import ServiceManagement

// Launch-at-login via SMAppService (macOS 13+). Registers the app bundle itself
// as a login item: no helper target, no LaunchAgent plist to author. One caveat:
// registration sticks most reliably when the app lives in /Applications, so
// `make install` copies it there before enabling.
enum LaunchAtLogin {
    static var isEnabled: Bool {
        SMAppService.mainApp.status == .enabled
    }

    /// Returns nil on success, or a human-readable error string.
    @discardableResult
    static func set(_ enabled: Bool) -> String? {
        let service = SMAppService.mainApp
        do {
            if enabled {
                if service.status != .enabled { try service.register() }
            } else {
                if service.status == .enabled { try service.unregister() }
            }
            return nil
        } catch {
            return "\(error.localizedDescription)"
        }
    }

    static var statusText: String {
        switch SMAppService.mainApp.status {
        case .enabled: return "on"
        case .notRegistered: return "off"
        case .requiresApproval: return "needs approval in System Settings › Login Items"
        case .notFound: return "off (app not found, try placing it in /Applications)"
        @unknown default: return "unknown"
        }
    }

    // MARK: - First-run onboarding

    /// Durable state dir, deliberately OUTSIDE ~/.config/gitwatchd so wiping config
    /// doesn't reset onboarding and silently re-enable launch-at-login after an opt-out.
    static var stateDir: String {
        (NSHomeDirectory() as NSString).appendingPathComponent("Library/Application Support/gitwatchd")
    }
    static var firstRunSentinel: String {
        (stateDir as NSString).appendingPathComponent("first-run-complete")
    }

    /// On the first launch of an *installed* copy, enable launch-at-login once:
    /// always-on is this app's whole point, so this meets the user's stated intent
    /// rather than sneaking past it. Gated so it:
    ///   • never fires for a dev build run from build/ (only /Applications or ~/Applications)
    ///   • never re-fires once onboarded, so a later opt-out sticks forever (rule #3)
    /// Returns a message to surface, or nil if nothing was done.
    @discardableResult
    static func enableOnFirstInstalledRunIfNeeded() -> String? {
        let bundlePath = Bundle.main.bundlePath
        let installedRoots = ["/Applications",
                              (NSHomeDirectory() as NSString).appendingPathComponent("Applications")]
        guard installedRoots.contains(where: { bundlePath.hasPrefix($0 + "/") }) else {
            return nil   // dev run from build/: never touch login items or the sentinel
        }
        guard !FileManager.default.fileExists(atPath: firstRunSentinel) else {
            return nil   // already onboarded: respect any later opt-out forever
        }
        let error = set(true)
        try? FileManager.default.createDirectory(atPath: stateDir, withIntermediateDirectories: true)
        FileManager.default.createFile(atPath: firstRunSentinel, contents: nil)
        return error.map { "launch-at-login could not be enabled: \($0)" }
            ?? "enabled launch-at-login (\(statusText))"
    }
}
