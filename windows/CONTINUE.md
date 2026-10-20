# Continue on another Windows computer

The repository contains code, launcher scripts and the non-secret sample
database. It does not contain this computer's passwords, customer secrets,
live database, OAuth credentials, conversation history or installed tools.

## Current computer: open Codex from CMD

From the parent workspace:

```bat
cd /d C:\Users\Bekbolat\Desktop\Anticode\CLIProxyAPI
START-CODEX.cmd
```

Enter the endpoint `http://127.0.0.1:8319/v1`, your customer `ck_...` key,
the model name and the project directory. The key is entered at a hidden
prompt and is not stored in the launcher preferences. Use ADMIN-PASSWORD.txt
only for the management website, never for this client.

On the current computer, a dedicated key is already in the private
`.keyrelay-runtime/CLIENT-API-KEY.txt` file. At the key prompt, press Enter to
use it with `http://127.0.0.1:8319/v1`. The launcher checks it with the server
before starting Codex. For other endpoints, enter their own key manually.
An empty input without a local key asks again instead of closing the launcher.
Rotating a key in the panel invalidates its previous value, including a key
already used by an open Codex window. Restart with the newest key afterward.

The client connects directly to the selected endpoint. The local server uses
ch-relay and the Plus account logged in through its Auth Files flow. The current
work computer has completed that login, and a real gpt-6-astra request plus a
real Codex CLI request have succeeded. A fresh computer must complete its own
installation and login. Changing the endpoint does not remove subscription
limits or create unlimited model usage.

## New computer

Install Git, Python 3.12+, Node.js 24 and Go 1.26+ if they are not installed.
On a Windows machine with winget, the initial commands are:

```bat
winget install --id Git.Git -e
winget install --id Python.Python.3.12 -e
winget install --id OpenJS.NodeJS.LTS -e
winget install --id GoLang.Go -e
```

After installing them, open a new CMD window. Bun 1.3.14 and Codex CLI can be
installed with:

```bat
npm install -g bun@1.3.14 @openai/codex@0.163.0
```

Clone the published task branch using the URL supplied with the handoff:

```bat
git clone --branch codex/windows-keyrelay https://github.com/bakenck1/CLIProxyAPI-keyrelay.git
cd CLIProxyAPI-keyrelay
windows\SETUP.cmd
windows\SETUP-RELAY.cmd
windows\LOGIN-PLUS.cmd
```

SETUP builds the server and the Windows plugins, obtains the pinned management
frontend, creates a fresh private runtime and starts the administration pages.
It downloads the official Zig 0.15.2 compiler with a checked SHA-256 digest.
Existing runtime credentials and databases are preserved. SETUP-RELAY then
downloads and tests a pinned public ch-relay source, installs its own Bun 1.4.3
with a SHA-256 check, starts its token-authenticated listener, enables VipPro,
and issues a dedicated 7-day CMD client key if no key file already exists.
LOGIN-PLUS opens the OpenAI login page. Complete sign-in with your Plus account
yourself, then leave the CMD window open for the verification result. It sends
one short gpt-6-astra request after login; READY requires the expected streamed
answer. This small check uses the account's allowance. If the login expires,
run LOGIN-PLUS again. No OpenAI Platform API key is needed by this flow.

Open:

- Management: <http://127.0.0.1:5173>
- API key administration: <http://127.0.0.1:8319/v0/resource/plugins/key-shop/admin>
- Customer page: <http://127.0.0.1:8319/client>

Use the new `.keyrelay-runtime/ADMIN-PASSWORD.txt` to log into the management
pages. The CMD key is in `.keyrelay-runtime/CLIENT-API-KEY.txt`; press Enter at
the launcher's key prompt to use it. Once it expires, create a new customer key
in API Keys and paste that key at the prompt, or replace this private key file
with the new value. Rotate immediately invalidates the previous key.

The additional relay console is <http://127.0.0.1:11504>, username `localadmin`,
with the same management password assigned when the relay was first installed.
Both relay ports are bound only to 127.0.0.1. The shared data listener at 11500
requires a valid ChatGPT bearer token; the CLI instead uses its customer key
through KEYRELAY at 8319. Do not enter a ck_ key into the relay console.

Then, in a second CMD:

```bat
cd /d C:\path\to\CLIProxyAPI-keyrelay
windows\CODEX.cmd
```

For future starts use `windows\START-SITE.cmd`. To pull and rebuild later changes:

```bat
git pull --ff-only
windows\STOP-SITE.cmd
windows\SETUP.cmd
windows\SETUP-RELAY.cmd
```

`127.0.0.1` always means the computer where the command runs. It does not
connect back to the computer at work. Customer keys issued by the work
installation are not automatically valid in the fresh home installation.
Log in to Plus on the home machine; do not copy somebody else's OAuth files.

To check the relay and complete one short real model test, run
`windows\CHECK-RELAY.cmd`. For the current parent workspace, the equivalent
root-level commands are START-SITE.cmd, LOGIN-PLUS.cmd and CHECK-RELAY.cmd.

## Relay source and boundaries

The compatible public relay is
[lemicoffee2024-collab/ch-relay](https://github.com/lemicoffee2024-collab/ch-relay),
pinned to `aa0b7cf1cbb254f9a692adc44303ee4fdc5df6dd`. Its authorship relationship
to the KEYRELAY author is unverified. The original handoff only names a local
Docker image; it does not provide a public image download. This setup runs the
reviewed source directly with Bun; Docker/WSL are not required, and no downloaded
relay source or binary is republished in this Git repository.

The wrapper disables automatic model substitution, old tool-output truncation,
reasoning stripping, prompt dumps and response caching. It does not patch the
global Codex settings, import other accounts or open a network-facing listener.
Its admin login is enabled. Live data is stored inside the private runtime.
The source's 111 tests and the Windows wrapper checks passed. Account access,
usage limits, upstream availability and model entitlements still apply.

## Validation

The admin login fix has focused authentication tests. Run them with:

```bat
cd examples\plugin\key-shop\ui\admin
npm ci
bun test tests\live.test.ts
npm run build
```

The main management frontend has existing verification failures, so this
setup runs its development server rather than deploying management.html.

The direct Codex launcher has an offline integration check:

```bat
python -m unittest discover -s windows/tests -p "test_*.py" -v
python windows\tests\check_codex_client.py
```

Client provider configuration follows the
[official Codex custom provider documentation](https://learn.chatgpt.com/docs/config-file/config-advanced#custom-model-providers).
