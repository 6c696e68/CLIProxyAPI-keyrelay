# Phân tích bảo mật ch-relay-vippro

## Context

Yêu cầu: phân tích plugin `ch-relay-vippro` có lỗ hổng nghiêm trọng hay không. Kết quả là báo cáo tiếng Việt dựa trên mã nguồn và kiểm chứng cục bộ an toàn, không tự sửa plugin hoặc thăm dò dịch vụ đang chạy. Đây là rà soát tập trung vào plugin và các ranh giới host mà plugin thực sự sử dụng, không phải kiểm toán toàn bộ CLIProxyAPI.

## Findings recorded during planning

- Mã nguồn ở `examples/plugin/ch-relay-vippro/go/main.go` và `stats.go`; module riêng có `main_test.go`, `go.mod`, `go.sum`. Tài liệu là `examples/plugin/ch-relay-vippro/README.md`.
- README dòng 3–7 mô tả plugin chuyển Chat Completions/Responses qua `host.http`, lấy Codex access token qua `host.auth.list`/`host.auth.get`, không refresh/save token. Không kết luận việc gửi token lên upstream là lỗi khi đó chính là chức năng được cấu hình.
- `main.go:568–624`: `validateBaseURL` chỉ chấp nhận HTTPS hoặc HTTP cho localhost/private IP, từ chối userinfo/query/fragment; `normalizeHeaders` từ chối tên đích `Authorization` và `Host`.
- `main.go:860–930,957–1033`: `execute` nhận model/payload/headers, chọn account từ host rồi `postModel` gửi POST tới `cfg.BaseURL + /chat/completions` hoặc `/responses`. Cần truy tiếp host transport và bảo vệ API đầu vào trước khi kết luận SSRF hay credential bypass.
- `main.go:1136–1209`: phản hồi thành công được chuyển tiếp; lỗi đi qua `failureMessage` có danh sách token đã dùng. Cần kiểm tra chính xác đường redaction và header/stream, không suy rộng thành rò rỉ token khi chưa có đầu vào gây lỗi.
- `main.go:435–457`: cấu hình lỗi được bỏ qua và giữ cấu hình trước; phải phân biệt lỗi vận hành với vi phạm ranh giới bảo mật có tác nhân thấp quyền.
- `internal/api/server_management.go:254–289` gọi `managementAvailable` và `s.mgmt.Middleware()` trước plugin management dispatch. `internal/api/handlers/management/handler.go:263–397` yêu cầu management key và kiểm tra remote policy. `stats.go:426–433` chỉ đăng ký hai management route, không đăng ký resource route công khai. Thiếu auth check bên trong plugin không tự tạo auth bypass.
- `stats.go:52–82,344–369` thực sự xuất `usage_by_email` trong stats và persistence; không được lặp lại khẳng định README rằng toàn bộ response không chứa email. Email ở route đã xác thực chưa phải lỗ hổng nghiêm trọng.
- `stats.go:546–565,629–673` ngăn scan trùng bằng `Running` và giới hạn 8 worker. `stats-file` đến từ cấu hình quản trị, không từ request; write dùng temporary file cùng thư mục rồi rename (`stats.go:372–399`).
- Host transport dùng `helps.NewProxyAwareHTTPClient` (`internal/pluginhost/http_bridge.go:217–225`) và `client.Do` (`144–178`); do đó URL validation ở plugin chưa chứng minh redirect an toàn. Host đọc toàn bộ body non-stream (`61–90`).
- Fixture sẵn có: `setup`, `fakeHost`, `httpScript`, `readChunk` trong `main_test.go:19–173`; không cần tạo seam/export mới. `TestStatusAndRedaction:1488–1548` chủ động cho token giả đi xuyên 2xx body nhưng che token trong lỗi. Đây là điểm kiểm chứng trust boundary, không được tự động gán severity nếu chưa chứng minh attacker ít quyền điều khiển upstream echo.
- Máy có Docker và sandbox-exec; danh sách Docker image hiện có không có image Go chuyên dụng. Chưa xác nhận sandbox chạy Go 1.26 + cgo được. Không pull/install hoặc thực thi target trong lúc lập kế hoạch.

## Approach

### 1. Establish the actual trust boundaries before rating anything

Trace the complete path from `/v1/chat/completions` and `/v1/responses` through the host auth middleware, plugin routing, `execute`, host auth callbacks, `host.http`, and the configured upstream. Record four principals separately: API caller, management caller, host/plugin operator, and upstream operator. Treat a behavior as a confirmed vulnerability only when a lower-trust principal can cause a concrete unauthorized disclosure, action, or service impact. Treat the hard-coded default `https://7656b154.linhtd.com/v1` as an explicit credential trust decision: every default-enabled request sends a host Codex bearer to that remote origin. Do not label it a vulnerability until ownership/trust of that origin is established, but report it prominently as a potentially high-impact condition.

### 2. Verify the protections already present and avoid false positives

Confirm from source and focused local fixtures that: only configured exact model names are claimed; the client `Authorization` header cannot replace the selected Codex bearer; configured `Authorization` and `Host` headers are rejected; base URLs are not request-derived and HTTP is restricted to loopback/private addresses; host HTTP operations are bound to the plugin callback instance; management routes run after management-key and remote-policy middleware; account scans are single-flight and capped at eight workers; and error messages redact selected bearer values. Preserve the distinction between configured-admin control and API-caller control: a malicious `base-url` or `stats-file` supplied only by an administrator is not an API vulnerability.

### 3. Resolve the two source-grounded candidates with bounded evidence

1. **Recursive JSON parser / availability:** trace all ingress body limits and nesting limits before `extractModel` (`main.go:1892–2123`). If no earlier bound exists, run a local fixture with dummy data and bounded nesting against the parser/plugin in an OS sandbox. Stop at the first reproducible parser error, stack exhaustion, or process termination. Classify only the observed impact; a request failure or bounded resource cost is not Critical/High, while process-wide termination requires evidence from the isolated host process. If the sandbox cannot enforce the required controls, record `needs_validation` rather than execute.
2. **Bearer trust/reflection:** use only dummy tokens and a local fixture upstream to establish that `postModel` sends the selected bearer and that successful bodies/stream chunks are returned unchanged (`main.go:957–968,987–1009,1212–1285`). This proves data flow, not a vulnerability. Separately identify who controls the default origin and whether ordinary API clients are allowed to use a shared auth pool. If the origin is untrusted or compromised relative to the host owner, classify the default bearer transfer as a confirmed high-impact credential-disclosure boundary; otherwise record it as an intentional but sensitive trust dependency. Do not contact the real origin or use real tokens.

### 4. Check shared-state and management edge cases

Inspect whether model cooldown, sticky bindings, stats email rows, and account-check callbacks permit one ordinary API caller to affect another account/model or disclose another tenant’s data. In particular, test the already-present `TestChatGPTModelCooldown` behavior with two configured model pools in a dummy fixture: `cooldownIndex` and `cooldownToken` are global maps, so determine whether an unsupported response for one model suppresses a different model for the same account. Report this as a concrete cross-request availability issue only if the fixture demonstrates it and the affected model/account is otherwise authorized for use. Keep `usage_by_email` as an authenticated-management privacy observation unless a management-auth bypass is demonstrated.

### 5. Produce the final evidence-based result

Return a concise Vietnamese report with: verdict on Critical/High findings; a severity table containing only confirmed findings; separate `needs_validation` items with the exact missing deployment fact; source path and line anchors; lower-trust actor; affected principal/resource; minimal safe reproduction result; and the smallest source/config fix. State explicitly that the review is scoped to this plugin and the host seams it uses, and do not claim full-repository coverage. If no Critical/High finding survives validation, say so directly while retaining the hard-coded remote bearer trust decision as the first operational warning.

## Critical files & anchors

- `examples/plugin/ch-relay-vippro/go/main.go:435–624,790–930` — configuration validation, model claiming, request entry, and upstream URL construction.
- `examples/plugin/ch-relay-vippro/go/main.go:957–1033,1212–1365` — bearer injection, retries, successful/error response forwarding, streaming, header/session handling, and redaction.
- `examples/plugin/ch-relay-vippro/go/main.go:1400–1553,1892–2159` — auth list/get selection, raw token extraction, recursive payload parser, and token redaction.
- `examples/plugin/ch-relay-vippro/go/stats.go:344–399,426–690` — management disclosure, stats-file persistence, account-check authorization boundary, cancellation, and worker cap.
- `internal/api/server_management.go:254–289`; `internal/api/handlers/management/handler.go:263–397`; `internal/pluginhost/host_callbacks.go:143–280`; `internal/pluginhost/http_bridge.go:61–178,217–225` — host-side API/management middleware, callback ownership, and HTTP transport behavior.

## Verification

- Before any target execution, verify an OS-enforced local-only sandbox is available. It must deny external network, start from `env -i` with only safe allowlisted variables, mount the repository read-only, provide a separate writable scratch directory for `GOCACHE`, `GOMODCACHE`, `TMPDIR`, and outputs, and enforce low CPU, memory, process, file-size, disk, and wall-clock limits. Do not install dependencies, pull images, access real auth files, or run a server.
- Use the existing module from `examples/plugin/ch-relay-vippro/go` with `GOPROXY=off` and a scratch cache. If dependencies or Go 1.26 are not already available inside the sandbox, stop execution and record the exact missing prerequisite; do not weaken the sandbox.
- Run the focused existing contract checks in the sandbox, not the whole suite by default: `go test ./... -run 'Test(RejectInvalidConfigKeepsPrevious|HTTPHeadersRetryAndURLs|StatusAndRedaction|ChatGPTModelCooldown|AccountCheckSkipsDisabled|StatsGetDoesNotListAuth)$' -count=1`. Expected observations: invalid URL/header config cannot replace the prior config; client Authorization is not forwarded; selected bearer is used upstream; failed responses redact the bearer; management GET does not trigger auth enumeration; and cooldown behavior matches the existing single-model contract.
- For the recursive parser, use a disposable dummy-input harness or the smallest existing test extension only in scratch; feed increasing nested arrays/objects while keeping the body bounded and stop at the first abnormal result. Expected safe outcome is a controlled invalid-payload error or bounded rejection, not host-process termination. Do not retain a new test or alter the worktree during this review.
- For bearer trust/reflection, use the existing `fakeHost`/`httpScript` fixture (`main_test.go:19–173`) with non-secret values. Verify the exact outbound URL, Authorization header, and returned 2xx body/stream. Expected result is that the configured upstream receives the dummy bearer and its successful response is returned unchanged; classify severity only after the origin-control fact is established.
- For cooldown isolation, configure two aliases and one dummy account, return the documented model-not-supported response for alias A, then call alias B. Expected secure isolation is that alias B still attempts the account; if it is skipped, record the exact cross-model impact and source root cause (`cooldownIndex`/`cooldownToken`) without modifying the plugin.
- Read-only source review and sandbox evidence are sufficient for the report. No live endpoint, real OAuth token, real stats file, external DNS, production account, or shared process may be contacted.

## Assumptions & contingencies

- The user wants a security assessment, not a patch. Do not edit plugin or host source, add permanent tests, build a release artifact, or deploy anything as part of this review.
- The default remote origin’s ownership and data-handling policy are not discoverable safely from this local source. If the owner cannot establish that it is trusted to receive Codex OAuth bearers, report the condition as `needs_validation` with the operational recommendation to disable the plugin or replace `base-url` before use; do not probe the origin.
- If management middleware is bypassed only in a particular deployment, report that as deployment-specific `needs_validation` and list the exact settings to inspect (`remote-management.allow-remote`, management secret, ingress/proxy reachability). Do not infer deployed values from source.
- If a local fixture shows only a controlled 4xx, memory growth, or client-visible upstream error, do not promote it to High/Critical. Severity must match the demonstrated principal and impact.
- If the sandbox cannot satisfy the security-audit execution policy, finish with source findings plus explicit validation blockers; do not claim runtime verification.
