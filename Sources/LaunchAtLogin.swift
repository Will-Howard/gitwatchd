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
            recordDesired(enabled)   // remember intent so reinstalls can restore it
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
    /// Records the state the user wants ("on"/"off"). Its absence doubles as
    /// the first-run marker. (Pre-record installs left an empty file; that
    /// predates opt-out recording and means "on".)
    static var desiredStateFile: String {
        (stateDir as NSString).appendingPathComponent("first-run-complete")
    }

    private static func recordDesired(_ on: Bool) {
        try? FileManager.default.createDirectory(atPath: stateDir, withIntermediateDirectories: true)
        try? (on ? "on" : "off").write(toFile: desiredStateFile, atomically: true, encoding: .utf8)
    }

    /// nil = never onboarded; true/false = the state the user last chose.
    private static var recordedDesired: Bool? {
        guard let s = try? String(contentsOfFile: desiredStateFile, encoding: .utf8) else { return nil }
        return s.trimmingCharacters(in: .whitespacesAndNewlines) != "off"
    }

    /// On every launch of an *installed* copy, make reality match recorded
    /// intent. First installed run: enable launch-at-login (always-on is this
    /// app's whole point) and record it. Later runs: if the user wants it on
    /// but the registration went stale (each ad-hoc re-sign gives the app a
    /// new identity, invalidating the old registration), quietly re-register.
    /// An opt-out is recorded as "off" and is never overridden. Dev builds
    /// from build/ never touch login items or the record.
    /// Returns a message to surface, or nil if nothing was done.
    @discardableResult
    static func reconcileOnInstalledRun() -> String? {
        let bundlePath = Bundle.main.bundlePath
        let installedRoots = ["/Applications",
                              (NSHomeDirectory() as NSString).appendingPathComponent("Applications")]
        guard installedRoots.contains(where: { bundlePath.hasPrefix($0 + "/") }) else {
            return nil
        }
        let firstRun = recordedDesired == nil
        guard recordedDesired ?? true else { return nil }   // opted out: never touch
        if !firstRun && isEnabled { return nil }            // wanted on, still on
        let error = set(true)
        if error != nil { recordDesired(true) }   // set records on success; keep intent on failure
        let what = firstRun ? "enabled launch-at-login" : "restored launch-at-login after reinstall"
        return error.map { "launch-at-login could not be enabled: \($0)" }
            ?? "\(what) (\(statusText))"
    }
}
