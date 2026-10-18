# Báo cáo rà soát `ch-relay-vippro` và `key-shop`

Ngày: 2026-10-17  
Phạm vi: mã Go, test, persistence SQLite, plugin lifecycle, management/API và luồng request/stream của hai plugin.

## Kết luận điều hành

Chưa thấy đường khai thác trực tiếp kiểu lộ secret hoặc bỏ qua xác thực management trong phạm vi mã đã đọc. Tuy nhiên, hệ thống **chưa nên coi là production-hardening hoàn chỉnh** vì `key-shop` có rủi ro làm sai hard quota trong điều kiện upstream trả usage lớn, còn `ch-relay-vippro` có chi phí latency và I/O cao trên mọi request.

Ưu tiên xử lý trước khi tải lớn:

1. **HIGH – key-shop có thể vượt token limit** khi usage thực tế lớn hơn reservation.
2. **HIGH – key-shop phụ thuộc tuyệt đối vào `RequestID`**, request thiếu ID có thể dùng chung reservation và làm mất accounting/finalize.
3. **HIGH – vippro retry cả nhiều lỗi không nên retry**, làm tăng latency và số lần gọi upstream.
4. **MEDIUM – vippro ghi stats đồng bộ xuống disk trên mỗi request**, khóa toàn cục trong thời gian I/O.
5. **MEDIUM – vippro đọc danh sách auth và có thể đọc auth file cho từng request**, tạo overhead O(số account).
6. **MEDIUM – các query danh sách key/request dùng `LIKE '%q%'` và một số bộ lọc không có index phù hợp**, sẽ chậm rõ khi dữ liệu tăng.

## Phát hiện chi tiết

### HIGH-1: Hard quota có thể bị vượt bởi một request đơn

**Vị trí:** `examples/plugin/key-shop/go/keyshop/store.go:524-541`, `:625-649`.

`Admit` chỉ giữ chỗ tối đa `ReserveTokens` (mặc định 32,768), nhưng `Finalize` cộng toàn bộ `input + output` vào `tokens_used` mà không giới hạn theo số đã reserve hoặc kiểm tra lại quota. Ví dụ key có limit 100, request được reserve 100 nhưng usage thực tế là 10,000; `Finalize` ghi thêm 10,000 và quota bị vượt. Điều này có thể xảy ra tự nhiên với input lớn hoặc usage báo sai/tăng đột biến từ upstream.

**Tác động:** mất tính đúng đắn của token cap và có thể phát sinh chi phí vượt mức bán cho khách hàng.

**Khuyến nghị:** lưu reservation amount cùng lúc finalize, sau đó áp dụng một policy rõ ràng:

- hard cap: chỉ bill phần còn lại trong limit và đánh dấu over-limit/failed; hoặc
- quota theo reservation: từ chối ngay các request có input đã biết vượt phần còn lại, đồng thời reservation phải bao phủ cả input + output dự kiến.

Cần thêm regression test: limit nhỏ, usage lớn hơn reservation, kết quả `tokens_used` không được vượt policy đã chọn.

### HIGH-2: `RequestID` rỗng làm hỏng tính idempotency/accounting

**Vị trí:** `store.go:542-544`, `:610-620`; `service.go:121-124`.

`reservations.request_id` là khóa chính. `Admit` không từ chối `in.RequestID == ""`; nhiều request thiếu ID sẽ cùng chèn khóa rỗng, và `ON CONFLICT DO NOTHING` khiến các lần sau vẫn được cho qua nhưng không có reservation riêng. Khi finalize với ID rỗng, request đầu tiên có thể ăn usage của các request khác; các request còn lại bị coi là đã finalize hoặc không còn reservation.

**Tác động:** quota/accounting sai, khó truy vết; nếu upstream/plugin contract có lúc không tạo ID thì đây là lỗi nghiêm trọng.

**Khuyến nghị:** reject rõ ràng khi `RequestID` trống ở `Admit`, `NoteUsage`, `Finalize`; hoặc tạo ID server-side trước khi admission và truyền cùng ID qua toàn bộ lifecycle. Nên có test nhiều admission đồng thời với ID rỗng và kỳ vọng lỗi rõ ràng.

### HIGH-3: Retry vippro quá rộng, làm tăng latency và upstream load

**Vị trí:** `examples/plugin/ch-relay-vippro/go/main.go:1037-1130`.

`performAttempts` tiếp tục thử account kế tiếp với hầu hết mọi non-2xx. Các lỗi client như 400/401/403/404 và nhiều 422 không được phân biệt với lỗi tạm thời. Với retry mặc định 3, một request lỗi có thể tạo nhiều HTTP call tuần tự trước khi trả về.

**Tác động:** p95/p99 tăng, dễ bị rate-limit upstream, tăng chi phí và làm lỗi cấu hình chậm phản hồi. Với stream, mỗi attempt lỗi còn có thể phải đọc hết stream trước khi retry.

**Khuyến nghị:** phân loại retry theo status/error code:

- không retry: 400, 401, 403, 404, 422 và lỗi payload/model cố định;
- cân nhắc retry: 408, 409, 425, 429, 5xx và lỗi transport;
- backoff có jitter cho 429/5xx;
- giới hạn tổng thời gian hoặc ngân sách retry, không chỉ số lần.

### MEDIUM-1: Ghi stats đồng bộ trên critical path

**Vị trí:** `examples/plugin/ch-relay-vippro/go/stats.go:176-200`, `:373-400`.

Mỗi request gọi `noteRequest`, giữ `statsMu`, serialize toàn bộ stats rồi tạo file tạm, ghi và rename. Đây là disk I/O đồng bộ trên đường hoàn thành request; dưới tải cao, các goroutine xếp hàng sau mutex và request latency phụ thuộc filesystem.

**Khuyến nghị:** cập nhật counters trong memory thật nhanh; dùng một writer goroutine/bounded channel flush theo batch hoặc interval (ví dụ 1–5 giây), flush khi shutdown. Cần policy khi queue đầy: gộp delta hoặc drop telemetry, tuyệt đối không chặn request vô hạn.

### MEDIUM-2: Auth discovery O(N) trên mỗi request vippro

**Vị trí:** `main.go:934`, `:1677-1743`, `:1746-1769`.

Mỗi execute gọi host auth list, sau đó có thể đọc auth JSON cho từng account để xác định plan/token. Đây là nhiều host callback tuần tự trên mỗi request, dù danh sách auth thường ít thay đổi.

**Khuyến nghị:** cache snapshot account/token ngắn hạn với TTL; invalidate khi plugin reconfigure hoặc host báo auth thay đổi. Không cache token quá lâu; dùng singleflight để tránh nhiều request đồng thời cùng refresh snapshot. Giữ đường fallback khi token hết hạn hoặc host trả lỗi.

### MEDIUM-3: Query tìm kiếm/list sẽ suy giảm khi dữ liệu lớn

**Vị trí:** `key-shop/go/keyshop/store.go:327-366`, `:697-720`.

Tìm key dùng `LIKE '%query%'` trên label/customer/id nên không dùng B-tree index. Requests dùng các predicate dạng `(?='' OR column=?)`, làm query planner khó tận dụng index. `OFFSET` cũng ngày càng đắt ở trang sâu.

**Khuyến nghị:**

- tìm theo prefix hoặc bổ sung FTS5 nếu cần contains search;
- xây query động chỉ thêm predicate khi filter có giá trị;
- dùng cursor pagination nhất quán thay cho OFFSET;
- đo bằng `EXPLAIN QUERY PLAN` với dữ liệu mô phỏng lớn trước khi chọn index.

### MEDIUM-4: Stream error body không có giới hạn bộ nhớ

**Vị trí:** `ch-relay-vippro/go/main.go:1182-1195`.

`readStreamBody` append toàn bộ chunk vào một slice trước khi retry/trả lỗi. Nếu upstream trả stream lỗi rất lớn hoặc không kết thúc, plugin có thể giữ nhiều memory và kéo dài thời gian retry.

**Khuyến nghị:** giới hạn bytes dùng để phân loại/làm error message (ví dụ 64 KiB), luôn đóng stream khi đạt giới hạn, và không cần giữ toàn bộ body chỉ để tìm một error marker.

### LOW: Một số điểm nên làm rõ/đo lường

- `writeStatsFile` tạo thư mục với `0755` (`stats.go:378`), trong khi file là telemetry có thể chứa email đã mask; nên dùng quyền thư mục chặt hơn nếu path nằm trong thư mục riêng.
- `countPath` dùng `strings.Contains` (`keyshop/store.go:981-983`), có thể phân loại nhầm path chứa chuỗi tương tự; nên normalize path và so khớp segment.
- `requestBaseURL` tin vào `X-Forwarded-Host`/`X-Forwarded-Proto` (`service.go:421-437`). Chỉ dùng được nếu upstream proxy đã sanitize header; nếu không, dashboard có thể hiển thị base URL giả.
- vippro đã có `validateBaseURL`, nhưng HTTPS hostname vẫn phụ thuộc trust của config admin; nếu config có nguồn không tin cậy, cần policy allowlist/egress restriction ở host.

## Tối ưu hiệu năng theo thứ tự lợi ích

1. Bỏ disk I/O khỏi request path của stats; batch flush và đo p95/p99.
2. Cache auth snapshot/token với TTL + singleflight; đo số host callback/request.
3. Thu hẹp retry matrix và thêm backoff/jitter; ghi histogram attempts/request.
4. Tối ưu key-shop query bằng dynamic predicates, cursor và index theo workload thực tế.
5. Nếu throughput key-shop cao, giảm số transaction/round-trip trong admission/finalize bằng prepared statements hoặc gộp các truy vấn kiểm tra tồn tại; vẫn giữ transaction atomic.
6. Đặt giới hạn body/stream để chống memory spike và giới hạn tổng retry time.

## Validation đã chạy

- `cd examples/plugin/key-shop/go && go test ./...` — pass.
- `cd examples/plugin/ch-relay-vippro/go && go test ./...` — pass.
- `cd examples/plugin/key-shop/go && go test -race ./...` — pass.
- `cd examples/plugin/ch-relay-vippro/go && go test -race ./...` — pass.
- `git diff --check` — pass.

Các test hiện có bảo vệ concurrency reservation và lifecycle cơ bản, nhưng chưa bao phủ hard quota khi actual usage vượt reservation, `RequestID` rỗng, retry theo status, stats flush contention, auth discovery overhead và stream body limit. Đây là các test nên bổ sung trước khi triển khai tối ưu.

## Verdict

**REQUEST CHANGES trước production scale-up** vì HIGH-1 và HIGH-2 ảnh hưởng trực tiếp đến quota/accounting. Sau khi sửa và thêm regression tests, các tối ưu MEDIUM có thể triển khai theo profiling; không nên tối ưu bằng cảm tính trước khi đo p95/p99, host callbacks/request và transaction latency.
