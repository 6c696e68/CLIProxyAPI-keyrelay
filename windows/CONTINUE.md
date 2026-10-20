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

The client connects directly to the selected endpoint. It does not add another
proxy or perform ChatGPT login. Model access requires a healthy upstream,
not just a customer key. The current administration-only install still lacks
the standalone ch-relay backend and its required Plus account login.

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
```

SETUP builds the server and the Windows plugins, obtains the pinned management
frontend, creates a fresh private runtime and starts the administration pages.
It downloads the official Zig 0.15.2 compiler with a checked SHA-256 digest.
Existing runtime credentials and databases are preserved. It does not install
the separate ch-relay backend. The UI starts in administration-only mode with
the unavailable relay disabled, and overall model status stays NOT READY.

Open:

- Management: <http://127.0.0.1:5173>
- API key administration: <http://127.0.0.1:8319/v0/resource/plugins/key-shop/admin>
- Customer page: <http://127.0.0.1:8319/client>

Use the new `.keyrelay-runtime/ADMIN-PASSWORD.txt` to log into the management
pages. Create a new customer key in API Keys. The sample database contains a
sample key hash, but its old plaintext secret is not available. Complete the
missing ch-relay setup and your own Auth Files login before expecting model
responses; see `../KEYRELAY-SETUP.md` for the full ready gates.

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
```

`127.0.0.1` always means the computer where the command runs. It does not
connect back to the computer at work. Customer keys issued by the work
installation are not automatically valid in the fresh home installation.
Log in to Plus on the home machine; do not copy somebody else's OAuth files.

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
python windows\tests\check_codex_client.py
```

Client provider configuration follows the
[official Codex custom provider documentation](https://learn.chatgpt.com/docs/config-file/config-advanced#custom-model-providers).
