# ch-relay-vippro config audit plan

Audit date: 2026-10-20. Read-only. No plugin or panel code was changed.

Scope: `examples/plugin/ch-relay-vippro` (schema, defaults, validation, hot-reload, host callbacks, management routes) and the panel editor in `Cli-Proxy-API-Management-Center` (`PluginConfigSheet`, `vipProConfigValidation.ts`, `PluginModelsEditor`).

Host path, verified: `internal/pluginhost/host.go` calls `plugin.reconfigure` on reload (`TestHostApplyConfig_ReconfigureCalledOnReload` in `internal/pluginhost/host_test.go`). `internal/pluginhost/config.go` `runtimeConfigYAML` sends the raw plugin subtree plus host `enabled` and `priority`. The plugin applies that in `configure` (`examples/plugin/ch-relay-vippro/go/main.go:449`) on both `plugin.register` and `plugin.reconfigure` (`main.go:421`). Request paths read the result through `currentConfig()` (`main.go:734`).

## Findings

### F1. Model catalog fields are parsed and then dropped

- Cite: `examples/plugin/ch-relay-vippro/go/main.go:138-146` (struct), `main.go:818-830` (`modelList` copies only `ID`, `Name`, `DisplayName`, `Thinking`).
- Panel still edits them: `Cli-Proxy-API-Management-Center/src/features/plugins/pluginModels.ts:34-42` and `:108-114`.
- README documents them as live: `examples/plugin/ch-relay-vippro/README.md:90`.
- Root cause: `modelSpec` stores `max-context-length`, `force-mapping`, `image`, `input-modalities`, `output-modalities`, `is-compat`, and `use-max-completion-tokens`, but `modelList` never copies them onto `pluginapi.ModelInfo`. The host type can carry context length and modalities (`sdk/pluginapi/types.go:173-182`) and has no field for `force-mapping`, `image`, `is-compat`, or `use-max-completion-tokens`. Saving those keys in the panel changes YAML and does not change the catalog or the upstream request.

### F2. `thinking` only honors `levels`

- Cite: `examples/plugin/ch-relay-vippro/go/main.go:849-869`.
- README: `examples/plugin/ch-relay-vippro/README.md:90` says an omitted `thinking` inherits canonical levels, and an explicit `thinking.levels` wins.
- Root cause: `thinkingFromSpec` returns nil unless `thinking.levels` is a non-empty string list. `min`, `max`, `zero-allowed`, `dynamic-allowed`, and `disable-type` in the same map are ignored. An explicit thinking object without `levels` falls through to `thinkingFromCanonical` (`main.go:841-844`), so the row does not actually override the canonical model.

### F3. `upstream-cache` is accepted and ignored

- Cite: `examples/plugin/ch-relay-vippro/go/main.go:131`. The only other mentions are the test fixture `go/main_test.go:1404` and an unrelated example `examples/keyrelay/config.example.yaml:73`.
- Root cause: `KnownFields(true)` (`main.go:494`) keeps the key so old YAML still loads, and `normalizeConfig` never reads `cfg.UpstreamCache`. It is not in `configFields()` (`main.go:763-777`), not in the README field table (`README.md:67-80`), and not in panel `FIELD_NAMES` (`vipProConfigValidation.ts:7-17`). The key looks configurable and does nothing.

### F4. `priority` is host plugin order, not an account knob

- Cite: parsed at `main.go:120`; absent from `configFields()` at `main.go:763-777`. Account selection uses the auth record: `main.go:1760` and `highestPriority` at `main.go:1954-1962`.
- README states this: `README.md:63`.
- Panel edits it as a separate sheet field: `Cli-Proxy-API-Management-Center/src/features/plugins/PluginConfigSheet.tsx:419-423` and `pluginConfigDraft.ts:102-109`.
- Root cause: the host injects `priority` into the plugin YAML (`internal/pluginhost/config.go:13`, `:61-66`). The plugin stores it and never reads `cfg.Priority`. That is correct for account choice, but the sheet label does not say it only orders plugins. A user can think it ranks Codex accounts.

### F5. Panel rejects numbers the plugin clamps

- Cite: plugin clamps at `main.go:549-552` and `main.go:568-580`. Panel rejects at `vipProConfigValidation.ts:107-109`.
- README matches the plugin: `README.md:73-77` (clamp, do not fail registration). Schema text matches too: `main.go:769`.
- Root cause: `request-retry` outside 0..32, `fallback-retry` outside 0..3, and `sticky-seconds` outside 0..86400 are stored clamped and the plugin stays registered (`TestRejectInvalidConfigKeepsPrevious` covers the keep-previous path; the clamp path is `main_test.go:516`). The sheet blocks Save on the same values. `model-cooldown-minutes` is not even in panel `FIELD_NAMES` (`vipProConfigValidation.ts:7-17`), so the sheet does not check its 0..10080 clamp at all.

### F6. Panel `plus-preference` is case-sensitive; the plugin is not

- Cite: plugin lowercases then accepts `ignore` at `main.go:535-546`. Panel checks the raw string at `vipProConfigValidation.ts:113-114` against `off|prefer|only|ignore` (`vipProConfigValidation.ts:6`).
- Test: `ONLY` becomes `only` at `main_test.go:463-464`.
- Root cause: `ONLY` or `Prefer` loads in the plugin and is rejected by the sheet. `ignore` is accepted by both and stored as `off`, but the English error text says only "off, prefer, or only" (`en.json:1725`) and the option list has no `ignore` (`en.json:1767-1771`).

### F7. Invalid YAML is a silent no-op

- Cite: `examples/plugin/ch-relay-vippro/go/main.go:449-462`. `plugin.register` / `plugin.reconfigure` still return success at `main.go:421-423`.
- Test: `TestRejectInvalidConfigKeepsPrevious` at `main_test.go:469-504`.
- Root cause: a bad `base-url`, unknown field, bad enum, or a second YAML document leaves the previous config in place. The first failed load uses `defaultPluginConfig()` (`main.go:474-486`), which is `enabled: true` with an empty model list. The host is not told the document was rejected, so the management UI can show the saved YAML while the process still runs the old one.

### F8. Default config is enabled and claims nothing

- Cite: `defaultPluginConfig` at `main.go:474-486` sets `Enabled: true`, default base URL, retry 3, fallback 0, `round-robin`, `prefer`, sticky 300. It does not set `ModelCooldownMinutes` (the nil pointer later means 1440 via `main.go:723-726`). Empty YAML takes this path at `main.go:489-491`.
- Host default for a missing plugin block is the opposite: `enabled: false` (`internal/pluginhost/config.go:13`). README agrees with the host: `README.md:34`.
- Root cause: two defaults. A real host load is fine because the host always sends `enabled`. A direct `configure` with empty bytes, which tests use (`main_test.go:400`), turns the plugin on with no models. `normalizeConfig` does not fill `ModelCooldownMinutes` on the struct, so a snapshot of the default config shows a nil cooldown even though requests use 1440.

### F9. README error table is missing a live code

- Cite: executor returns `accounts_cooling` at `main.go:983`. The table at `README.md:178-187` lists `disabled`, `unknown_model`, `invalid_payload`, `unsupported_format`, `no_plus_account`, `no_account`, `auth_unavailable`, `upstream_error`. It does not list `accounts_cooling`.
- Root cause: the code path is newer than the table. When every selected account is in model cooldown and fallback is off, clients get a code the README does not name.

### F10. Panel and README disagree about what `request-retry` pays for

- Cite: attempts are built as accounts × planned names, capped by `1+retry` at `main.go:992`. Fallback is a separate budget at `main.go:994-997`.
- README: `README.md:73` says extra attempts on the next account with the same upstream model. `README.md:74` says fallback does not spend `request-retry`.
- Panel hint: `en.json:1748` says the budget is "shared by switching accounts and upstream model names" and that an overload hidden in HTTP 200 spends it.
- Root cause: the hint describes a shared account-and-model budget. The code keeps fallback on `fallback-retry` only. Operators tuning the sheet will set the wrong knob for model fallback.

### F11. `stats-file` and cooldown persistence have no panel check

- Cite: trim and side-file load at `main.go:553` and `main.go:467-468`. Cooldown file is `stats-file + ".cooldowns.json"` per README `README.md:78`.
- Panel: `stats-file` is translated (`en.json:1777-1779`) but omitted from `FIELD_NAMES` (`vipProConfigValidation.ts:7-17`), so `applyChRelayVipProErrors` never writes a `stats-file` error. `model-cooldown-minutes` has schema text (`main.go:774`) and no English field block under `ch_relay_vippro.fields`.
- Root cause: both keys round-trip through the generic sheet. A non-integer cooldown or a stats path the process cannot write is not flagged next to the other vippro errors. The plugin itself only trims the path; a bad path fails later inside the stats writer, not at configure time.

### F12. Prefix with a slash is emptied, not rejected

- Cite: `normalizePrefix` at `main.go:617-623`. Test expects `a/b` to become `""` at `main_test.go:441-442`.
- README: `README.md:71` says that value "claims nothing". Panel sets an error instead: `vipProConfigValidation.ts:104-105`.
- Root cause: the sheet refuses a config the plugin accepts. Accepted means the prefix is dropped, so client IDs lose the prefix and the plugin still claims the bare alias (`clientID` at `main.go:811-815`). The two UIs do not describe the same outcome.

## What already matches

- Hot reload works. Host `plugin.reconfigure` reaches `configure`, which swaps `runtime.cfg` under `runtimeMu` and reloads stats and cooldowns (`main.go:464-468`). Execute reads `currentConfig()` once per request (`main.go:944`).
- Base URL rules match: https, or http only for localhost, loopback, private, and `100.64.0.0/10` (`main.go:583-614` and `vipProConfigValidation.ts:22-49`). Userinfo, query, and fragment are rejected on both sides.
- Headers: `$NAME` and `$CPA-SESSION-ID` are documented; `Authorization` and `Host` are rejected (`main.go:637-639`, `vipProConfigValidation.ts:60-73`).
- Defaults that both sides share when the field is omitted: `request-retry` 3, `fallback-retry` 0, `sticky-seconds` 300, `rotation-mode` `round-robin`, `plus-preference` `prefer`, `model-cooldown-minutes` 1440, max cooldown 10080, max sticky 86400.
- The plugin does not refresh Codex tokens. It calls `host.auth.list` and `host.auth.get` (`main.go:1705`, `main.go:1833`) and `host.http`. Management routes stay `GET /v0/management/ch-relay-vippro/stats` and `POST /v0/management/ch-relay-vippro/accounts/check`. The dashboard remains the plugin resource iframe.

## Processing-logic findings

Compared with sibling `examples/plugin/ch-relay/go/main.go`. That plugin does not pick Codex accounts itself. Its schema says rotation, plus preference, and sticky TTL are host-side (`ch-relay/go/main.go:236-238`), and POST chat/responses are never replayed (`ch-relay/go/main.go:239-242`). Vippro does the opposite: it lists auth, picks an account, and replays the POST. That split is intentional. The findings below are places where vippro's own replay contradicts its config or the README, not a request to copy ch-relay's host-side policy.

Request-time read: `execute` copies `currentConfig()` once (`main.go:944`) and passes that struct into `postModel`, `runAttempts`, and `openStream`. Base URL, headers, rotation mode, plus preference, and retry budgets for that request stay on the snapshot. Good. The exceptions are called out in F13, F16, and F18.

### F13. Account-check `cooling` ignores model cooldown

- Cite: `classifyEnabled` at `examples/plugin/ch-relay-vippro/go/stats.go:879-893` counts cooling only from `NextRetryAfter`. Model cooldown lives in `cooled` at `main.go:1236-1246` and is what `splitCooldown` uses at `main.go:1225-1233`.
- README: `README.md:164` defines `cooling` as enabled records still before `next_retry_after`. `README.md:78` separately says a model refusal skips that account for `model-cooldown-minutes`.
- Root cause: `POST .../accounts/check` and the 30-minute scanner never call `cooled`. An account skipped for a day after "model is not supported" still increments `eligible` (and `plus` when the bearer is Plus). The stats object and the dashboard disagree with the pool `execute` will actually use. The background scan also calls `host.auth.list` with an empty callback id (`stats.go:716`, `stats.go:790`), so a failed list leaves `running: false` and the previous counters (`stats.go:791-792`).

### F14. Sticky binds before the account succeeds

- Cite: provisional bind at `main.go:2064-2068`. Success bind at `main.go:1399-1414`, called only after 2xx at `main.go:1113-1114`.
- README: `README.md:123` says sticky keeps one account until `sticky-seconds` elapses. `README.md:127` says sticky is recorded for the account that actually succeeded, and repeating a success does not extend the deadline.
- Root cause: `stickyOrderLocked` writes `runtime.sticky[session]` to the first planned account before any HTTP call, with `now + sticky-seconds`. A failure still leaves that bind. The next request for the session sticks to the account that just failed until the deadline. `bindSticky` on success refuses to extend an unexpired bind for the same index (`main.go:1408-1409`), so a later success does not repair the early deadline. `sticky-seconds: 0` still writes that provisional bind, then `bindSticky` returns immediately (`main.go:1400`), so a zero TTL does not mean "sticky off" for the first attempt.

### F15. `request-retry` spends one budget on accounts and upstream names

- Cite: `buildAttempts` at `main.go:2144-2173`. Primary budget is `1+cfg.retry()` at `main.go:992`. Name rotation happens first in `planNamesLocked` at `main.go:2082-2092`.
- README: `README.md:73` says extra attempts stay on the same upstream model and the next account. `README.md:88` says several rows with the same alias rotate upstream names.
- Root cause: the walk is `accounts[step%n]` with `names[step%n]`, then a second nested fill. With two accounts, two names, and `request-retry: 1` (budget 2), the first two attempts are account0/name0 and account1/name1. The second attempt already changed the upstream model. That matches the panel hint in F10 and contradicts the README. Fallback names are not in this budget; they have `fallback-retry`.

### F16. A 2xx model refusal still counts as success and sticks

- Cite: `performAttempts` at `main.go:1102-1127`. Stream scan cools later in `forwardStream` at `main.go:1522-1534` without unpinning.
- README: `README.md:96` says the ChatGPT model-not-supported error inside a 2xx stream cools the account. `README.md:120` says one upstream error moves round-robin to the next account. `README.md:131` says the plugin does not read a 2xx body to switch accounts.
- Root cause: on HTTP 2xx the loop marks cooldown if the body matches, then still `pinRoundRobin(index, false)` and `bindSticky`. The client already has a 2xx, so not switching mid-response matches the README, but the rotation state treats the refusal as the account that "succeeded". The 2xx path records a success pin and a sticky bind that are ignored until the account returns to the ready slice: `splitCooldown` removes it, and `stickRoundRobinLocked` only searches that ready slice (`stickRoundRobinLocked` only searches the ready slice, `main.go:1160-1173`). Sibling ch-relay never replays a POST and does not keep this pin.

### F17. Model-unavailable classification is a substring test

- Cite: `modelUnavailable` at `main.go:1199-1205`. Fallback starts only when `modelMissing` is set or every primary token was skipped (`main.go:1069-1070`).
- README: `README.md:92` limits fallback to the ChatGPT model-not-supported error, `model_not_found`, or "model ... does not exist".
- Root cause: any body that contains both "model" and "does not exist" sets `modelMissing`, even when the missing object is not the model (a tool, a file, a response id). One such response is enough to set the flag for the whole primary loop, so a later 429 does not clear it, and fallback runs after the budget. A 400 that says "model is required" does not match, which is fine. The false positive is the loose "does not exist" pair.

### F18. Cooldown reload merges and never replaces

- Cite: `loadCooldowns` at `main.go:1296-1327`, called from `configure` at `main.go:468`. `markModelCooldown` reads `runtime.cfg.StatsFile` at `main.go:1274` instead of the `cfg` argument used for the duration.
- Root cause: reconfigure with a new `stats-file` loads that file's records on top of the in-memory maps. Old index and token deadlines stay until they expire, including deadlines that belonged only to the previous file. Reconfigure to an empty `stats-file` returns at `main.go:1297-1298` and leaves memory untouched, so cooldown keeps working with nowhere to persist. `markModelCooldown` can also persist to a path that changed after the request snapshot was taken, because it re-reads `runtime.cfg` under the lock. Duration correctly uses the snapshot (`cfg.modelCooldown()`).

### F19. Plus file cache stores the access token

- Cite: `tokenFromPath` at `main.go:1799-1824` and `plusFileCache` at `main.go:1702`.
- Root cause: the cache key is path, mtime, and size, which is the right invalidation for plan detection. The value is `cachedPlan{token, plus}`, so the raw Codex access token stays in the plugin process for the life of the process. `readAuth` via `host.auth.get` is not cached (`main.go:1832-1856`), which is the path that sees a refresh. A token rotated in place with the same size and mtime keeps serving the cached bearer. The plugin never writes auth and never calls a refresh callback; this cache is the only extra copy.

### F20. Alias pools advertise one upstream name

- Cite: `modelList` sets `Name: pool.names[0]` at `main.go:826`. `planNamesLocked` rotates those names at `main.go:2082-2092`. `resolveModel` treats a bare upstream name as explicit and skips rotation (`main.go:925-932`).
- README: `README.md:88` says several rows with the same alias are one catalog model and the plugin rotates the upstream names.
- Root cause: `/v1/models` shows only the first row's `name`. Clients that call the alias rotate. Clients that call the bare upstream name pin that name (`explicit` true) and still use that row's fallback only (`main.go:929-932`). The catalog and the alias path disagree, and a direct name bypasses the pool rotation the README describes.

## Tasks

Do these in order. Task 1 is the only behavior change that can affect a live request. Tasks 2–4 are contract fixes. Tasks 5–7 are panel and docs.

### Task 1. Advertise the model fields the host can carry

- Files: `examples/plugin/ch-relay-vippro/go/main.go`, `examples/plugin/ch-relay-vippro/go/main_test.go`.
- Change: in `modelList`, copy `MaxContextLength` to `ContextLength`, and copy `InputModalities` / `OutputModalities` to `SupportedInputModalities` / `SupportedOutputModalities`, from the first row of each pool (same rule as `DisplayName`). Leave `force-mapping`, `image`, `is-compat`, and `use-max-completion-tokens` unchanged in this task: `pluginapi.ModelInfo` has nowhere to put them. Do not change account selection, base URL, or token use.
- Test: extend the model-list test so a row with `max-context-length: 128000` and `input-modalities: [text, image]` appears on the `plugin.model.static` payload. Assert `force-mapping: true` still does not add a field.
- Risk: clients that read `/v1/models` context length will start seeing the configured number instead of 0. That is the intended correction.
- Accept: `go test -count=1 -run 'TestUnconfiguredModelInheritsCanonicalThinkingLevels|TestModel' ./examples/plugin/ch-relay-vippro/go/` exits 0, and the new assertion fails if the copy is removed.

### Task 2. Make explicit `thinking` win, or document that only `levels` does

- Files: `examples/plugin/ch-relay-vippro/go/main.go`, `examples/plugin/ch-relay-vippro/go/main_test.go`, `examples/plugin/ch-relay-vippro/README.md`.
- Change: pick one. Preferred: `thinkingFromSpec` maps `min`, `max`, `zero-allowed`, `dynamic-allowed`, and `disable-type` when present, and still uses `levels` when present. If `levels` is empty but another key is set, do not fall through to the canonical levels. Alternative, if the product choice is "levels only": change README line 90 so it says other thinking keys are ignored.
- Test: a row `thinking: {min: 0, max: 100}` must not inherit canonical levels. A row with `thinking.levels: [low, high]` must still return those levels.
- Risk: rows that set only `min` today silently inherit canonical levels. After the preferred change they publish the explicit range instead.
- Accept: `go test -count=1 -run 'TestUnconfiguredModelInheritsCanonicalThinkingLevels' ./examples/plugin/ch-relay-vippro/go/` exits 0.

### Task 3. Drop or honor `upstream-cache`

- Files: `examples/plugin/ch-relay-vippro/go/main.go`, `examples/plugin/ch-relay-vippro/go/main_test.go`, `examples/plugin/ch-relay-vippro/README.md`.
- Change: preferred: remove the field from `pluginConfig` and delete `upstream-cache: false` from `TestUpstreamCacheKeepsStatsAndModels`. Unknown-field rejection already covers a stale key (`KnownFields(true)`). Do not invent a cache. Alternative: add the key to `configFields()` and the README as "accepted, ignored".
- Test: keep `TestUpstreamCacheKeepsStatsAndModels` asserting prefix, base URL, models, and stats load. Add a case that a document with only `upstream-cache: false` fails decode and does not replace a good config, if the field is removed.
- Risk: existing YAML that contains `upstream-cache` will fail the next reload and keep the previous config (F7). Call that out in the README before removing the field.
- Accept: `go test -count=1 -run 'TestUpstreamCacheKeepsStatsAndModels|TestRejectInvalidConfigKeepsPrevious' ./examples/plugin/ch-relay-vippro/go/` exits 0.

### Task 4. Surface a rejected reconfigure

- Files: `examples/plugin/ch-relay-vippro/go/main.go`, `examples/plugin/ch-relay-vippro/go/main_test.go`, `examples/plugin/ch-relay-vippro/README.md`.
- Change: `configure` should return the decode error. `handleMethod` for `plugin.register` and `plugin.reconfigure` should return that error instead of `okEnvelope` when decode fails. Keep the previous `runtime.cfg` on failure, exactly as today. Do not switch the first-load fallback to `enabled: false` in this task unless a test is updated. In the README, next to the F8 note, record that `defaultPluginConfig` is `enabled: true` while the host default for a missing plugin block is `enabled: false`.
- Test: `TestRejectInvalidConfigKeepsPrevious` currently requires a nil error (`main_test.go:500-501`). Change it so the call returns an error and `configSignature()` is unchanged. Add an empty-YAML case that still records the default config.
- Risk: the host treats a failed reconfigure as a plugin error and may keep the previous registration. That matches the in-process behavior already. Confirm `internal/pluginhost/host.go` around the `registerRPCPlugin` error path before shipping, and do not change host code in this task.
- Accept: `go test -count=1 -run 'TestRejectInvalidConfigKeepsPrevious' ./examples/plugin/ch-relay-vippro/go/` exits 0.

### Task 5. Align the panel with clamp, case, prefix, cooldown, and stats

- Files: `Cli-Proxy-API-Management-Center/src/features/plugins/vipProConfigValidation.ts`, the six locale files under `src/i18n/locales/` (`en.json`, `zh-CN.json`, `zh-TW.json`, `vi.json`, `ko.json`, `ru.json`), `PluginConfigSheet.tsx` only if the priority hint cannot live in locales, `examples/plugin/ch-relay-vippro/go/main.go` for the schema sentence at `configFields` (`main.go:769`).
- Change:
  - Compare `rotation-mode` and `plus-preference` case-insensitively. Keep accepting `ignore`.
  - Add `model-cooldown-minutes` (0..10080, empty means 1440) and a non-empty `stats-file` path check (trimmed, no NUL) to `FIELD_NAMES`.
  - Decide clamp versus reject for the three integer fields and make the hint match. Preferred: keep rejecting out-of-range values in the sheet, and change the plugin schema description so it says the panel rejects them. Do not clamp in the sheet and also reject.
  - Prefix: if the plugin keeps emptying a slash prefix, the sheet should warn with the README outcome ("claims the alias with no prefix"), not a hard error that blocks a config the plugin accepts. If product wants a hard error, change `normalizePrefix` in a later plugin task, not here.
  - Priority label: state that it orders plugins and does not rank Codex accounts.
  - Rewrite the `request-retry` hint so it does not say fallback shares that budget (F10).
- Test: the panel package's existing vippro validation test, or a new one next to `vipProConfigValidation.ts`, for `ONLY`, cooldown `10081`, cooldown empty, and a slash prefix.
- Risk: locales can drift. Add the new keys to all six files in one change. A stricter sheet will block saves that reload today.
- Accept: from `Cli-Proxy-API-Management-Center`, `npx vitest run src/features/plugins/vipProConfigValidation` exits 0. If that path does not exist, run the test file this task adds and record its path in the commit message.

### Task 6. Fix the README contract

- Files: `examples/plugin/ch-relay-vippro/README.md`.
- Change: add `accounts_cooling` to the error table. State that `upstream-cache` is ignored or removed, matching Task 3. State that `priority` is host plugin order. State the thinking rule from Task 2. State which model row fields actually reach the catalog after Task 1, and name the four that do not (`force-mapping`, `image`, `is-compat`, `use-max-completion-tokens`). Mention that a rejected reload keeps the previous config until Task 4 changes the return.
- Test: no Go test. Diff the README against this plan and check every field in the table exists on `pluginConfig` or is listed as host-injected.
- Risk: docs-only. Low.
- Accept: `git diff -- examples/plugin/ch-relay-vippro/README.md` shows the error code, the priority sentence, and the thinking sentence, and does not document a token refresh.

### Task 7. Regression command for the whole plugin

- Files: none new. Run after Tasks 1–4.
- Change: none.
- Test: `gofmt -l examples/plugin/ch-relay-vippro/go/*.go` prints nothing, then `go test -count=1 ./examples/plugin/ch-relay-vippro/go/` exits 0.
- Risk: none.
- Accept: both commands exit 0. Do not build the `.so` and do not copy it to the router.

### Task 8. Count model cooldown in accounts/check

- Files: `examples/plugin/ch-relay-vippro/go/stats.go`, `examples/plugin/ch-relay-vippro/go/main_test.go`.
- Change: `classifyEnabled` must call `cooled(index, token, now)` after it has a token. A cooled account increments `cooling`, not `eligible` or `plus`. Keep `NextRetryAfter` as the other cooling input. Do not change the stats or accounts/check route paths. Document that an empty callback id on the timer scan can fail the list and leave the previous snapshot.
- Test: mark a Plus account cooled, run the check classifier, and assert `cooling` is 1 and `plus` is 0. An uncooled Plus account still increments `eligible` and `plus`.
- Risk: the dashboard `cooling` number will rise for model refusals. That matches the pool `execute` already uses.
- Accept: `go test -count=1 -run 'TestAccount' ./examples/plugin/ch-relay-vippro/go/` exits 0.

### Task 9. Bind sticky only after success

- Files: `examples/plugin/ch-relay-vippro/go/main.go`, `examples/plugin/ch-relay-vippro/go/main_test.go`.
- Change: delete the provisional write in `stickyOrderLocked` (`main.go:2064-2068`). Keep the cursor rotation for an unbound session. `bindSticky` remains the only writer, and it already runs only after a 2xx. `sticky-seconds: 0` must not create a bind. Do not extend an unexpired bind when the same account succeeds again.
- Test: sticky mode, first account returns non-2xx, second succeeds. The following request must not prefer the failed account. A second success on the same account must keep the original `until`. `sticky-seconds: 0` must leave `runtime.sticky` empty.
- Risk: a session with no prior success rotates like fill-first until one account returns 2xx. That matches README line 127.
- Accept: `go test -count=1 -run 'TestSticky' ./examples/plugin/ch-relay-vippro/go/` exits 0.

### Task 10. Keep `request-retry` on one upstream name

- Files: `examples/plugin/ch-relay-vippro/go/main.go`, `examples/plugin/ch-relay-vippro/go/main_test.go`, `examples/plugin/ch-relay-vippro/README.md`.
- Change: `buildAttempts` for the primary budget walks accounts against `names[0]` first, then the remaining names only if the budget is not spent. Fallback names stay on `fallback-retry`. Update the panel `request-retry` hint in Task 5 so it no longer says the budget is shared with upstream names. Do not copy sibling ch-relay's "never replay POST" rule; vippro's account replay is the product behavior.
- Test: two accounts, two alias rows, `request-retry: 1`. Both primary attempts must send the same upstream name. A model-not-supported pair with `fallback-retry: 1` must then send the fallback name, not a second primary name.
- Risk: alias pools stop changing model inside the account-retry budget. Name rotation still starts at `planNamesLocked`, so the next request can pick the other name.
- Accept: `go test -count=1 -run 'TestFallback|TestRetry' ./examples/plugin/ch-relay-vippro/go/` exits 0.

### Task 11. A model refusal must not pin round-robin or sticky

- Files: `examples/plugin/ch-relay-vippro/go/main.go`, `examples/plugin/ch-relay-vippro/go/main_test.go`.
- Change: in the 2xx branch, if `chatgptModelUnsupported(body)` then `pinRoundRobin(index, true)` and skip `bindSticky`. Do not switch the client off that 2xx body. In `forwardStream`, a mid-stream match must call the same pin-failed path once. Cooldown marking stays.
- Test: 2xx body with the ChatGPT model-not-supported text, round-robin, two accounts. The next request's first bearer must be the other account. Repeat with sticky and assert the session does not bind the refused account.
- Risk: a refusal no longer counts as the sticky winner. Clients still receive the original 2xx bytes.
- Accept: `go test -count=1 -run 'TestChatGPTModelCooldown' ./examples/plugin/ch-relay-vippro/go/` exits 0.

### Task 12. Tighten model-unavailable matching

- Files: `examples/plugin/ch-relay-vippro/go/main.go`, `examples/plugin/ch-relay-vippro/go/main_test.go`.
- Change: keep the ChatGPT model-not-supported check and a `model_not_found` code check. Replace the loose "model" plus "does not exist" test with a single phrase that includes the model, such as `model` immediately followed by words and `does not exist`. A body about a missing tool or response id must not set `modelMissing`. One non-matching error later in the same loop must not be required to clear the flag; the flag must not be set from those bodies at all.
- Test: body `response id does not exist` with `fallback-retry: 1` must not call the fallback model. Body `model gpt-x does not exist` must.
- Risk: a novel upstream sentence that used to trip fallback will stay on the primary model and return the upstream error. That is the README rule.
- Accept: `go test -count=1 -run 'TestFallback' ./examples/plugin/ch-relay-vippro/go/` exits 0.

### Task 13. Replace cooldown maps on reconfigure

- Files: `examples/plugin/ch-relay-vippro/go/main.go`, `examples/plugin/ch-relay-vippro/go/main_test.go`.
- Change: `loadCooldowns` replaces `cooldownIndex` and `cooldownToken` with the records from the new file. An empty `stats-file` clears both maps. `markModelCooldown` persists with the stats path captured from the request `cfg`, not a fresh read of `runtime.cfg`.
- Test: configure file A with one live cooldown, reconfigure file B with none, assert the old index is not cooled. Reconfigure to an empty `stats-file` and assert `cooled` is false.
- Risk: in-flight cooldowns that were not flushed to the new file disappear on reload. That is the correct reading of "the skip is saved beside stats-file".
- Accept: `go test -count=1 -run 'TestCooldown' ./examples/plugin/ch-relay-vippro/go/` exits 0.

### Task 14. Stop caching the raw access token

- Files: `examples/plugin/ch-relay-vippro/go/main.go`, `examples/plugin/ch-relay-vippro/go/main_test.go`.
- Change: `plusFileCache` stores only `plus bool`. `tokenFromPath` may return the token on a cache miss after a disk read, and on a hit must return `("", plus, false)` so `accountToken` loads the bearer through `host.auth.get`. Do not add a refresh call. Do not log the token.
- Test: two `loadEligible` calls with the same path, mtime, and size. The second must still call `host.auth.get` (or otherwise not reuse a token that was overwritten in the auth JSON while mtime and size stay the same). Assert the cache value's type has no token field.
- Risk: one extra `host.auth.get` per eligible account per request after the first disk read. Plan detection still avoids re-reading the file.
- Accept: `go test -count=1 -run 'TestPlus|TestEligible' ./examples/plugin/ch-relay-vippro/go/` exits 0.

### Task 15. Publish every upstream name in an alias pool

- Files: `examples/plugin/ch-relay-vippro/go/main.go`, `examples/plugin/ch-relay-vippro/go/main_test.go`, `examples/plugin/ch-relay-vippro/README.md`.
- Change: for a pool with more than one `name`, set the advertised model `Name` to the alias, and put the upstream names in `Description` or stop claiming a single native name. Keep `resolveModel` explicit-name behavior, and say in the README that calling a bare upstream name pins that name. Do not add names to a host `/v1/models` injection path outside this plugin's `modelList`.
- Test: two rows, same alias, different names. `plugin.model.static` must not report only the first name as the sole native name. A request that uses the second bare name still sends that name.
- Risk: clients that read `model.name` and expected the first upstream name will see the alias instead. The alias is what they already call.
- Accept: `go test -count=1 -run 'TestModel|TestAlias' ./examples/plugin/ch-relay-vippro/go/` exits 0.

### Task 16. Regression after the processing tasks

- Files: none new. Run after Tasks 8–15.
- Change: none.
- Test: `gofmt -l examples/plugin/ch-relay-vippro/go/*.go` prints nothing, then `go test -count=1 ./examples/plugin/ch-relay-vippro/go/` exits 0.
- Risk: none.
- Accept: both commands exit 0. Do not build the `.so` and do not copy it to the router.

## Task 3 changed

Task 3's preferred removal was reverted. Production config for the v8030 instance still sets `upstream-cache: false`. `KnownFields(true)` would reject that document and the plugin would keep a config with no models. The field is accepted again, deprecated, and ignored. It stays out of `configFields()`.

## Verified not a defect

- Task 4 was not implemented. `internal/pluginhost/host.go` `callRegister` (`host.go:1252-1268`) treats a `plugin.reconfigure` error as an invalid plugin and drops it. Returning the decode error would unregister vippro on a bad reload. The plugin still keeps the previous config and returns success. The README now says that.
- F12 stays. A slash prefix is emptied and claims the bare alias. The panel still blocks that value. Both outcomes are documented. Changing either side would reject configs the other side accepts.
- F4 stays. `priority` is host plugin order. Account priority comes from the auth record. The README states that.

## Implemented

Tasks 1, 2, 3, 5, 6, and 8 through 16 are done with the preferred option. Task 7 and Task 16 are the regression commands below.

## Out of scope

- No edits under `/v0/management`.
- No change to host relay callbacks. This plugin keeps calling `host.auth.list`, `host.auth.get`, and `host.http`. It still does not refresh Codex tokens.
- No change to `GET /v0/management/ch-relay-vippro/stats` or `POST /v0/management/ch-relay-vippro/accounts/check`.
- The dashboard stays a plugin resource iframe.
- Do not touch `~/.cliproxyapi` config or auth.
- Do not edit other plugins. Dirty `key-shop` files in the worktree are not part of this audit.
