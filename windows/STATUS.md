# Handoff status

The Windows administration site and customer-key pages run locally. The
key-shop login now rejects customer keys before a network request and shows
the actual server ban/error message. Failed remembered credentials are cleared.
Use ADMIN-PASSWORD.txt for management and a newly issued ck_ key for clients.
Editing ACCESS.txt changes documentation, not the server password.

The work machine now runs a compatible public ch-relay source on 127.0.0.1:11500
with its authenticated admin console on 127.0.0.1:11504. VipPro is enabled and
the user completed local Plus OAuth login. Both a real streamed gpt-6-astra
request and an installed Codex CLI request through KEYRELAY succeeded. The
work installation reached READY. Fresh machines must perform their own login.

The public relay source is lemicoffee2024-collab/ch-relay, pinned to
aa0b7cf1cbb254f9a692adc44303ee4fdc5df6dd; the relationship to the KEYRELAY
author is unverified. The wrapper downloads it at setup time instead of
vendoring or redistributing it. It uses a separately pinned Bun 1.4.3 archive
with verified SHA-256. Docker/WSL are not required. Account login, model access,
limits and provider availability cannot be guaranteed by the launcher.

The customer page's model list represents configured key policy; it does not
prove that those models can generate responses. Unlimited local key quotas
also do not establish unlimited provider or subscription usage.

Portable entry points:

- windows/SETUP.cmd: build and start local administration on a fresh machine.
- windows/START-SITE.cmd: start/reuse the prepared administration processes.
- windows/CODEX.cmd: direct endpoint + client key + model + project prompts.
- windows/SETUP-RELAY.cmd: download, test and connect the pinned local relay.
- windows/LOGIN-PLUS.cmd: user-controlled OAuth login and real model check.
- windows/CHECK-RELAY.cmd: current readiness and a short streamed model check.

The scripts use Python 3.12+, Node.js 24, Go 1.26+, Bun 1.3.14 and Zig 0.15.2.
The pinned management frontend commit is
002f1e6a16c70ccc717387e9c46d6bec4ecc4365. Main frontend production verification
has pre-existing failures, so it is served in development mode at port 5173.
The backend and embedded key-shop UI are at port 8319. Both bind to loopback.

Validation performed: five admin authentication tests, admin TypeScript/Vite
build, Windows DLL/server compilation, a real Codex CLI fixture integration,
and portable setup build checks. No live provider tokens are used by the
fixture test. All 111 public relay tests passed, as did Windows wrapper
credential/ownership/readiness checks. Key-shop Go tests pass on Windows excluding the POSIX file-mode
test; the real runtime database's Windows ACL was checked separately.

Runtime credentials, live databases, logs, tools and history are intentionally
ignored by Git. Set up fresh credentials at home. Do not upload ACCESS.txt,
ADMIN-PASSWORD.txt, .secrets, auth JSON, or a live customer database.
