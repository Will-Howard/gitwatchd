# Upstream reference copy

`gitwatch.sh` is a verbatim copy of upstream gitwatch, used as the model in the
differential parity tests (`Tests/ParityTests.swift`): the same scenario runs
through this script and through our engine, and the resulting git state must
be identical.

- Source: https://raw.githubusercontent.com/gitwatch/gitwatch/master/gitwatch.sh
- Fetched: 2026-07-18
- sha1: a30bef63591f2a8477a31618016da9be3bdfefa7

Do not edit the script. To update it, re-fetch from upstream, update this
README, and re-run `make test` to see whether upstream changed behaviour.
