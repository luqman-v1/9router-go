### Fixed

- **Transparent retry for dead keep-alive sockets** — Automatically retries requests once on a fresh socket when an idle keep-alive connection was dropped by an upstream edge before response bytes were received (upstream decolua/9router@963787f73 parity).