import Network

/// Fires a callback when the network comes back after an outage, so repos stuck
/// on a failed push can retry immediately instead of waiting out their backoff.
/// Uses NWPathMonitor, Apple's reachability API: it reports a path status of
/// .satisfied whenever some interface can route traffic, and calls the handler
/// on every transition.
final class NetworkMonitor {
    private let monitor = NWPathMonitor()
    private let queue = DispatchQueue(label: "gitwatchd.network")
    private var wasSatisfied: Bool?   // nil until the first path update
    private let onRegain: () -> Void

    init(onRegain: @escaping () -> Void) {
        self.onRegain = onRegain
    }

    func start() {
        monitor.pathUpdateHandler = { [weak self] path in
            guard let self else { return }
            let satisfied = path.status == .satisfied
            // Only a genuine offline-to-online transition counts; the initial
            // update just records the starting state.
            if satisfied, self.wasSatisfied == false { self.onRegain() }
            self.wasSatisfied = satisfied
        }
        monitor.start(queue: queue)
    }
}
