# Handoff status

The Windows administration site and customer-key pages run locally. The
key-shop login now rejects customer keys before a network request and shows
the actual server ban/error message. Failed remembered credentials are cleared.
Use ADMIN-PASSWORD.txt for management and a newly issued ck_ key for clients.
Editing ACCESS.txt changes documentation, not the server password.

The standalone ch-relay backend is not included in this repository and was
not available at localhost:11500. Its distribution/image and launch
instructions are still needed from the author. The ch-relay plugin in this
repository is not that standalone backend. Own-machine Codex Plus login via
Auth Files is also outstanding. ch-relay-vippro stays disabled until these
prerequisites are satisfied. The model backend is NOT READY.

The customer page's model list represents configured key policy; it does not
prove that those models can generate responses. Unlimited local key quotas
also do not establish unlimited provider or subscription usage.

Portable entry points:

- windows/SETUP.cmd: build and start local administration on a fresh machine.
- windows/START-SITE.cmd: start/reuse the prepared administration processes.
- windows/CODEX.cmd: direct endpoint + client key + model + project prompts.

The scripts use Python 3.12+, Node.js 24, Go 1.26+, Bun 1.3.14 and Zig 0.15.2.
The pinned management frontend commit is
002f1e6a16c70ccc717387e9c46d6bec4ecc4365. Main frontend production verification
has pre-existing failures, so it is served in development mode at port 5173.
The backend and embedded key-shop UI are at port 8319. Both bind to loopback.

Validation performed: five admin authentication tests, admin TypeScript/Vite
build, Windows DLL/server compilation, a real Codex CLI fixture integration,
and portable setup build checks. No live provider tokens are used by the
fixture test. Key-shop Go tests pass on Windows excluding the POSIX file-mode
test; the real runtime database's Windows ACL was checked separately.

Runtime credentials, live databases, logs, tools and history are intentionally
ignored by Git. Set up fresh credentials at home. Do not upload ACCESS.txt,
ADMIN-PASSWORD.txt, .secrets, auth JSON, or a live customer database.
