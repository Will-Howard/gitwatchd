import Foundation
import ServiceManagement

// Launch-at-login via SMAppService (macOS 13+). Registers the app bundle itself
// as a login item — no helper target, no LaunchAgent plist to author. One caveat:
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
}
