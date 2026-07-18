import AppKit
import CoreServices

// gitwatchd: one binary, two modes:
//   • launched as gitwatchd.app (or `gitwatchd serve`) → menu-bar daemon (this file)
//   • run with CLI args (e.g. `gitwatchd .`)           → CLI.run (CLI.swift)
// The menu-bar daemon watches ~/.config/gitwatchd/repos.txt, live-reloads on edit,
// and auto-commits each repo on a debounce via native FSEvents + git.

// MARK: - Menu-bar daemon

final class AppDelegate: NSObject, NSApplicationDelegate {
    private var statusItem: NSStatusItem!
    private var watchers: [RepoWatcher] = []
    private var configErrors: [ConfigError] = []
    private var brokenRepoPaths: [String] = []
    private var configWatcher: FileWatcher?
    private var networkMonitor: NetworkMonitor?

    func applicationDidFinishLaunching(_ notification: Notification) {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        if let button = statusItem.button {
            button.image = NSImage(systemSymbolName: "arrow.triangle.2.circlepath",
                                   accessibilityDescription: "gitwatchd")
            button.image?.isTemplate = true
        }

        Config.ensureExists()
        if let envError = GitRuntime.resolved.error {
            NSLog("gitwatchd: shell environment capture FAILED: %@", envError)
        }
        if let msg = LaunchAtLogin.reconcileOnInstalledRun() {
            NSLog("gitwatchd: %@", msg)
        }
        reload()

        // When the network comes back after an outage, retry any repo stuck on
        // a failed push right away instead of waiting out its backoff.
        networkMonitor = NetworkMonitor { [weak self] in
            DispatchQueue.main.async {
                self?.watchers.filter { $0.wantsNetworkRetry }.forEach { $0.retryNow() }
            }
        }
        networkMonitor?.start()

        // Live-reload when the config file (or CLI) changes it.
        configWatcher = FileWatcher(path: Config.dir) { [weak self] in
            DispatchQueue.main.async { self?.reload() }
        }
        configWatcher?.start()

        // Refresh relative times / pending counts while the menu is open. Also
        // re-check broken config entries: they can heal without the config
        // changing (permission granted, volume mounted, repo re-created), and
        // a one-shot verdict at reload would otherwise stay stale forever.
        Timer.scheduledTimer(withTimeInterval: 5, repeats: true) { [weak self] _ in
            guard let self else { return }
            if self.brokenRepoPaths.contains(where: { Git.isRepo($0) }) { self.reload() }
            else { self.rebuildMenu() }
        }
    }

    /// Re-read config and reconcile the running watchers. Entries that can't
    /// be watched (unparseable line, missing path, not a git repo) are kept as
    /// config errors for the menu; a config line must never vanish silently.
    private func reload() {
        let previousPaths = Set(watchers.map { $0.path })
        let previouslyPaused = Set(watchers.filter { $0.paused }.map { $0.path })
        watchers.forEach { $0.stop() }
        let (watchable, errors) = Config.load()
        configErrors = errors
        brokenRepoPaths = errors.compactMap { $0.repoPath }
        watchers = watchable.map { spec in
            RepoWatcher(spec: spec) { [weak self] _, _ in
                DispatchQueue.main.async { self?.rebuildMenu() }
            }
        }
        watchers.forEach { $0.start() }
        for w in watchers where !w.paused {
            let resumed = previouslyPaused.contains(w.path)
            let newlyWatched = !previousPaths.contains(w.path)
            if resumed || (newlyWatched && w.spec.commitOnStart) { w.flushNow() }
        }
        rebuildMenu()
    }

    // MARK: Menu

    private func rebuildMenu() {
        updateIcon()
        let menu = NSMenu()

        // If the login-shell environment failed to load, say so loudly: pushes would
        // silently use the wrong git/PATH otherwise.
        if let envError = GitRuntime.resolved.error {
            add(menu, "⚠ shell environment failed to load", enabled: false)
            add(menu, "   \(envError)", enabled: false)
            menu.addItem(.separator())
        }

        if watchers.isEmpty && configErrors.isEmpty {
            add(menu, "No repos watched yet", enabled: false)
        } else {
            add(menu, "Watching \(watchers.count) repo\(watchers.count == 1 ? "" : "s")", enabled: false)
            menu.addItem(.separator())
            for w in watchers {
                let branch = Git.currentBranch(w.path, gitDir: w.spec.gitDir)
                let pending = Git.pendingCount(w.path, gitDir: w.spec.gitDir)
                // "name · branch", with at most one status tail; error detail
                // stays out of the main menu and lives in the submenu.
                let title = StatusFormat.rowTitle(
                    name: w.spec.name, branch: branch, paused: w.paused,
                    pending: pending, error: w.lastError?.outcome)
                let row = NSMenuItem(title: title, action: nil, keyEquivalent: "")
                row.submenu = repoSubmenu(for: w)
                menu.addItem(row)
                add(menu, "     \(Git.lastCommitSummary(w.path, gitDir: w.spec.gitDir))", enabled: false)
            }
            for e in configErrors {
                let row = NSMenuItem(title: StatusFormat.configErrorRow(label: e.label, reason: e.reason),
                                     action: nil, keyEquivalent: "")
                let sub = NSMenu()
                add(sub, summarize(e.detail), enabled: false)
                add(sub, "⚠ " + e.reason, enabled: false)
                row.submenu = sub
                menu.addItem(row)
            }
        }

        menu.addItem(.separator())
        add(menu, "Open Config File", action: #selector(openConfig))
        add(menu, "Copy Config Path", action: #selector(copyConfigPath))
        menu.addItem(.separator())
        // A checkmark via .state forces AppKit to reserve a left gutter column for the
        // whole menu; show the enabled state in the title instead so rows stay flush-left.
        let login = NSMenuItem(title: LaunchAtLogin.isEnabled ? "Launch at Login  ✓" : "Launch at Login",
                               action: #selector(toggleLaunchAtLogin), keyEquivalent: "")
        login.target = self
        menu.addItem(login)
        add(menu, "Quit gitwatchd", action: #selector(quit))

        statusItem.menu = menu
    }

    private func repoSubmenu(for w: RepoWatcher) -> NSMenu {
        let sub = NSMenu()
        add(sub, summarize(w.path), enabled: false)   // head-truncated so rows stay narrow
        if let err = w.lastError {
            sub.addItem(.separator())
            add(sub, StatusFormat.errorHeadline(label: err.outcome.errorLabel ?? "failing",
                                                attempts: err.attempts), enabled: false)
            if let detail = err.outcome.detail, !detail.isEmpty {
                add(sub, "   " + StatusFormat.truncated(detail), enabled: false)
            }
            add(sub, "   " + StatusFormat.retryLine(lastTried: err.lastAttempt,
                                                    nextRetry: err.nextRetry, now: Date()),
                enabled: false)
            addAction(sub, "Retry Now", #selector(retryNow(_:)), repo: w)
        }
        sub.addItem(.separator())
        addAction(sub, w.paused ? "Resume Watching" : "Pause Watching", #selector(togglePause(_:)), repo: w)
        sub.addItem(.separator())
        addAction(sub, "Copy Path", #selector(copyRepoPath(_:)), repo: w)   // one-click copy
        addAction(sub, "Open in Finder", #selector(openInFinder(_:)), repo: w)
        return sub
    }

    // MARK: Actions

    @objc private func togglePause(_ s: NSMenuItem) {
        guard let w = s.representedObject as? RepoWatcher else { return }
        // Pause is config-level state so it survives restarts: rewrite the
        // repo's config line and rebuild from it (reload flushes on resume).
        Config.setPaused(matching: w.path, paused: !w.paused)
        reload()
    }
    @objc private func retryNow(_ s: NSMenuItem) {
        (s.representedObject as? RepoWatcher)?.retryNow()
    }
    @objc private func openInFinder(_ s: NSMenuItem) {
        guard let w = s.representedObject as? RepoWatcher else { return }
        NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: w.path)])
    }
    @objc private func copyRepoPath(_ s: NSMenuItem) {
        guard let w = s.representedObject as? RepoWatcher else { return }
        copyToPasteboard(w.path)
    }
    @objc private func toggleLaunchAtLogin() {
        if let err = LaunchAtLogin.set(!LaunchAtLogin.isEnabled) {
            let alert = NSAlert()
            alert.messageText = "Couldn't change Launch at Login"
            alert.informativeText = err + "\n\nTip: run “make install” so gitwatchd lives in /Applications, which makes this reliable."
            alert.runModal()
        }
        rebuildMenu()
    }
    @objc private func openConfig() { NSWorkspace.shared.open(URL(fileURLWithPath: Config.path)) }
    @objc private func copyConfigPath() { copyToPasteboard(Config.path) }
    @objc private func quit() { NSApplication.shared.terminate(nil) }

    // MARK: Helpers

    /// Swap the menu-bar glyph to the attention variant while any repo is in an
    /// error state, back to the plain sync arrows when all are healthy.
    private func updateIcon() {
        let failing = watchers.contains { $0.lastError != nil } || !configErrors.isEmpty
        let symbol = failing ? "exclamationmark.arrow.triangle.2.circlepath"
                             : "arrow.triangle.2.circlepath"
        if let button = statusItem.button {
            button.image = NSImage(systemSymbolName: symbol,
                                   accessibilityDescription: failing ? "gitwatchd: attention needed"
                                                                     : "gitwatchd")
            button.image?.isTemplate = true
        }
    }

    private func copyToPasteboard(_ s: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(s, forType: .string)
    }

    /// Head-truncate a long path to 60 chars, keeping the meaningful tail: `…/deep/repo`.
    private func summarize(_ path: String) -> String {
        guard path.count > 60 else { return path }
        return "…" + path.suffix(59)
    }

    private func add(_ menu: NSMenu, _ title: String, enabled: Bool = true,
                     action: Selector? = nil) {
        let item = NSMenuItem(title: title, action: action, keyEquivalent: "")
        item.isEnabled = enabled
        if action != nil { item.target = self }
        menu.addItem(item)
    }

    private func addAction(_ menu: NSMenu, _ title: String, _ action: Selector, repo: RepoWatcher) {
        let item = NSMenuItem(title: title, action: action, keyEquivalent: "")
        item.target = self
        item.representedObject = repo
        menu.addItem(item)
    }
}

// MARK: - Minimal FSEvents directory watcher (used to live-reload the config)

final class FileWatcher {
    private let path: String
    private let onChange: () -> Void
    private var stream: FSEventStreamRef?
    private var pending: DispatchWorkItem?
    private let queue = DispatchQueue(label: "gitwatchd.config")

    init(path: String, onChange: @escaping () -> Void) {
        self.path = path
        self.onChange = onChange
    }

    func start() {
        var ctx = FSEventStreamContext(version: 0,
            info: Unmanaged.passUnretained(self).toOpaque(),
            retain: nil, release: nil, copyDescription: nil)
        let cb: FSEventStreamCallback = { _, info, _, _, _, _ in
            let me = Unmanaged<FileWatcher>.fromOpaque(info!).takeUnretainedValue()
            me.pending?.cancel()
            let work = DispatchWorkItem { me.onChange() }
            me.pending = work
            me.queue.asyncAfter(deadline: .now() + 0.3, execute: work) // debounce editor saves
        }
        stream = FSEventStreamCreate(kCFAllocatorDefault, cb, &ctx,
            [path] as CFArray,
            FSEventStreamEventId(kFSEventStreamEventIdSinceNow),
            0.2, UInt32(kFSEventStreamCreateFlagFileEvents))
        guard let stream else { return }
        FSEventStreamSetDispatchQueue(stream, queue)
        FSEventStreamStart(stream)
    }
}

// MARK: - Entry point: choose CLI vs GUI

let userArgs = Array(CommandLine.arguments.dropFirst()).filter { !$0.hasPrefix("-psn_") }
let launchedAsApp = Bundle.main.bundlePath.hasSuffix(".app")

if userArgs.first == "serve" || (launchedAsApp && userArgs.isEmpty) {
    // Daemon mode: re-derive the user's login-shell env for git (PATH, SSH, helpers).
    GitRuntime.isDaemon = true
    let app = NSApplication.shared
    let delegate = AppDelegate()
    app.delegate = delegate
    app.setActivationPolicy(.accessory) // menu-bar only, no Dock icon
    app.run()
} else {
    exit(CLI.run(userArgs))
}
