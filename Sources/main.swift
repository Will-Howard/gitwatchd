import AppKit
import CoreServices

// gitwatchd — one binary, two modes:
//   • launched as gitwatchd.app (or `gitwatchd serve`) → menu-bar daemon (this file)
//   • run with CLI args (e.g. `gitwatchd .`)           → CLI.run (CLI.swift)
// The menu-bar daemon watches ~/.config/gitwatchd/repos.txt, live-reloads on edit,
// and auto-commits each repo on a debounce via native FSEvents + git.

// MARK: - Menu-bar daemon

final class AppDelegate: NSObject, NSApplicationDelegate {
    private var statusItem: NSStatusItem!
    private var watchers: [RepoWatcher] = []
    private var lastActivity: [String: String] = [:] // path -> status word
    private var configWatcher: FileWatcher?

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
        if let msg = LaunchAtLogin.enableOnFirstInstalledRunIfNeeded() {
            NSLog("gitwatchd: first run — %@", msg)
        }
        reload()

        // Live-reload when the config file (or CLI) changes it.
        configWatcher = FileWatcher(path: Config.dir) { [weak self] in
            DispatchQueue.main.async { self?.reload() }
        }
        configWatcher?.start()

        // Refresh relative times / pending counts while the menu is open.
        Timer.scheduledTimer(withTimeInterval: 5, repeats: true) { [weak self] _ in
            self?.rebuildMenu()
        }
    }

    /// Re-read config and reconcile the running watchers.
    private func reload() {
        watchers.forEach { $0.stop() }
        watchers = Config.specs()
            .filter { Git.isRepo($0.path) }
            .map { spec in
                RepoWatcher(spec: spec) { [weak self] watcher, status in
                    DispatchQueue.main.async {
                        self?.lastActivity[watcher.path] = status
                        self?.rebuildMenu()
                    }
                }
            }
        watchers.forEach { $0.start() }
        rebuildMenu()
    }

    // MARK: Menu

    private func rebuildMenu() {
        let menu = NSMenu()

        // If the login-shell environment failed to load, say so loudly — pushes would
        // silently use the wrong git/PATH otherwise.
        if let envError = GitRuntime.resolved.error {
            add(menu, "⚠ shell environment failed to load", enabled: false)
            add(menu, "   \(envError)", enabled: false)
            menu.addItem(.separator())
        }

        if watchers.isEmpty {
            add(menu, "No repos watched yet", enabled: false)
            add(menu, "Add one:  gitwatchd .", enabled: false)
        } else {
            add(menu, "Watching \(watchers.count) repo\(watchers.count == 1 ? "" : "s")", enabled: false)
            menu.addItem(.separator())
            for w in watchers {
                let branch = Git.currentBranch(w.path)
                let pending = Git.pendingCount(w.path)
                // "name · branch", with a status tail only when there's something to say.
                var title = "\(w.spec.name) · \(branch)"
                if w.paused { title += " · ⏸ paused" }
                else if pending > 0 { title += " · ✎ \(pending) pending" }
                let row = NSMenuItem(title: title, action: nil, keyEquivalent: "")
                row.submenu = repoSubmenu(for: w)
                menu.addItem(row)
                add(menu, "     \(Git.lastCommitSummary(w.path))", enabled: false)
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
        w.paused.toggle(); rebuildMenu()
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

    private func copyToPasteboard(_ s: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(s, forType: .string)
    }

    /// Head-truncate a long path to `max` chars, keeping the meaningful tail: `…/deep/repo`.
    private func summarize(_ path: String, max: Int = 60) -> String {
        guard path.count > max else { return path }
        return "…" + path.suffix(max - 1)
    }

    private func add(_ menu: NSMenu, _ title: String, enabled: Bool = true,
                     action: Selector? = nil, key: String = "") {
        let item = NSMenuItem(title: title, action: action, keyEquivalent: key)
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
