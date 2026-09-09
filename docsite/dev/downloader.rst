下载器内部架构（downloader / outbound workers / userkeys）
================================================================

本文描述当前实现：qatlasd 内的本地优先 PDF 策略梯、PostgreSQL 持久化的
outbound multi-worker 协调器、独立 ``downloaderworker``，以及保留的
legacy ``downloaderproxy`` 与 ``internal/userkeys``。部署与 app 接入
背景见 :doc:`prod-deploy`、:doc:`apps`。对应源码说明位于
``docs/server/downloader-fleet.md``、``docs/server/downloader-workers.md``
与 ``internal/downloadfleet/README.md``。

本地优先与 RemoteFetcher 边界
--------------------------------------------------------------------------------

``internal/downloader`` 只依赖 ``RemoteFetcher`` 接口（``Enabled``、
``FetchPDF``），不直接依赖 fleet 服务。qatlasd 注入
``downloadfleet.Service``；worker 复用同一策略梯，但不启用远端委派或
agent，避免递归分发。协调器就在 qatlasd 内，**所有 worker 控制与传输
连接均由 worker 主动发起，master 不回拨 worker**。

``FetchPDF`` 接受带 DOI / arXiv 身份的 ``registry.PaperRef``，按下列
顺序尝试；trace 记录策略、URL、错误与耗时。这里的 local-first 不表示
必须耗尽本地 browser/agent 才委派：授权墙信号会提前触发远端。

.. list-table::
   :header-rows: 1
   :widths: 25 75

   * - 策略 / 分支
     - 当前顺序与行为
   * - ``arxiv``
     - 优先使用共享 arXiv fetcher，解析并固定版本；只有 arXiv 而无 DOI
       且本地失败时，可直接委派 fleet（此分支不调用 legacy proxy）。
   * - ``pmc-resolve``
     - PMCID 经 Europe PMC 解析真实 DOI，先试其 render 候选；解析失败
       直接返回错误，不保证所有失败都进入远端。
   * - ``twin-resolve``
     - DOI-only 先经 OpenAlex 找 arXiv twin，再尝试 arXiv。
   * - ``oa:europepmc`` / ``oa:unpaywall`` / ``oa:openalex`` /
       ``oa:semanticscholar``
     - 按顺序查询 OA 候选；仓储落地页先挖真实链接，trace 加
       ``+landing``，不是并行广播下载。
   * - ``pattern`` / ``landing``
     - 按 DOI 前缀猜测出版社直链，再解析 doi.org 落地页中的
       ``citation_pdf_url`` / PDF 链接；包含 IEEE stamp iframe 解析。
   * - ``remote-worker``（或 legacy ``remote-proxy``）提前位
     - trace 出现 challenge / 授权墙信号时，先于本地 browser 委派。
       远端终态失败后仍可继续本地兜底；pending 则立即返回等待语义。
   * - ``browser`` / agent backend
     - 有 challenge 或无 PDF 候选时，配置的 CDP lane 重放候选；有落地页
       且配置 agent 时由 OpenAI 兼容端点或 claude CLI 提名 URL，仍须验证。
   * - 远端终位
     - 上述失败且 trace 尚无 ``remote-worker`` / ``remote-proxy`` 时
       最后委派一次，不重复创建本轮远端尝试。

``tryRemote`` 保留整个 ``FetchOutcome``，包括解析后的 DOI/arXiv 身份、
``RemoteTaskID``、``WorkerID``、trace，而非只复制 PDF body：

- ``Archived=true`` 表示 fleet 已完成对象存储与 registry 注册，
  ``Result.Body`` 可以为空；本地 ``process`` 不能再次落库或再触发 hooks。
- ``ErrRemotePending`` 与 ``Pending=true`` 表示持久化任务仍可恢复。
  数据库结果不确定、调用者取消或等待超时不是终态失败；本地 admission
  保持 queued / ``remote_wait``。取消等待不会取消已经持久化的任务。
- fleet 先读取 ``done`` / ``failed``，再判断存储的执行 deadline，因而
  超期后仍能读到既有终态。staged 归档可以晚于执行 deadline 完成。

验证、实际存储对象与 provenance
--------------------------------------------------------------------------------

普通 FetchClient 候选使用 ``%PDF-``、10 KiB–100 MiB 尺寸界与尾部
2 KiB 的 ``%%EOF`` 检查；存在 ``startxref`` 时可豁免缺失的尾部 EOF。
HTML 会分类为 bot/PoW challenge、paywall、error page 等，驱动后续策略。
每个 FetchClient 有 cookie jar、带 ``QAtlasDownloader`` 标记的浏览器 UA、
per-host 默认 0.5 rps / burst 1；429/5xx 最多额外重试一次（2s 退避，
Retry-After 上限 8s）。``respect_robots`` 默认 false；开启后的解析为
fail-open、10 分钟缓存，不是 worker 的网络隔离机制。

**不要把本地验证与 fleet 验证描述为完全相同。** fleet 上传及 staged
恢复独立核对准确大小、SHA-256、最小尺寸和 ``%PDF-``，要求最后 4 KiB
内有 ``%%EOF``，没有 ``startxref`` 豁免。因此某些本地通过的 PDF
仍可能被 master 拒收。上述结构检查也不证明论文内容与标识符语义相符。

``storeOutcome`` 用 canonical DOI/arXiv asset key 和
``IfNoneMatch: "*"`` 写对象；新对象 metadata 包括 ``sha256``、
``source=downloader:<strategy>``、``source_url``、``fetched_by`` 与
``fetched_at``。fleet 成功策略带 ``worker:<worker-id>:<strategy>``。
条件写冲突时不能把新候选的哈希、大小或 URL 当作旧对象的来源：

- 实际 GET 现有 canonical 对象，流式计算真实哈希与大小，并通过
  ``inspectStoredPDF`` 的签名、10 KiB–100 MiB、尾部 2 KiB EOF /
  ``startxref`` 检查；不信任对象 metadata 中宣称的摘要。
- registry 登记的是实际留在对象存储里的那份字节。来源 URL 读取
  现有对象 ``source_url``；缺失就保持未知，不借用失败候选的 URL。
  现有对象不会因本次上传而被覆盖。
- fleet attempt receipt 仍保留**原始接受上传**的 SHA-256 / size，
  不随 canonical 冲突改写；worker 才能识别自己那份已获确认的文件。

worker metadata 会移除来源 URL 的 userinfo/query/fragment，以通用错误
摘要替代任意 downloader 错误文本，最多导出 12 条 trace。master 校验
结果身份：arXiv 必须同 stem 且匹配显式版本，DOI 必须同一 DOI；当前
``resultIdentity`` **不接受 DOI task 被 arXiv 结果替换**。共享策略梯能
发现 twin 不等于 v2 接受这种跨身份上传，这是当前实现边界。

持久化 admission 与单一恢复所有者
---------------------------------

fleet 模式下，``Downloader.Enqueue`` 在启动本地策略前先经
``DownloadJournal.SaveDownloadRequest`` 写入 ``downloader_requests``。
因此进程在远端委派之前退出，也不会仅剩一条内存队列记录。

``request_id`` 是 admission generation：同一 paper 尚 queued 时重复提交
复用它；终态之后显式用户重试才生成新 ID。``WithAdmissionID`` 将其
贯穿本地任务与远端请求；``download_fleet_admissions`` 把 generation
持久链接到 fleet task。恢复优先查该链接，即使原 task 已 done/failed
也读同一终态，不能因重启重新获得 worker-attempt 预算。新的 admission
可复用仍活跃的 identity task，或在旧 task 终态后建立新 task。

archive/hook callback 携带最新链接的 admission ID；journal 完成写入
同时匹配 ``paper_id`` 与 ``request_id``，旧 callback 不能完成较新的
显式请求。该 journal 不保存 worker 密钥或浏览器会话。

本地执行并发默认 2；fleet 等待释放 local permit，再恢复本地策略时
重新获取。执行 goroutine 数为本地并发加有界 remote waiters（默认 6），
``scheduled`` 按 paper 去重、涵盖排队与执行，上限 128。超出该窗口的
已接受工作留在 PostgreSQL，不为整个 backlog 创建无界等待 goroutine。

qatlasd 在持有单进程 data lock 后，用 ``downloadBackground`` 共同拥有
``fleet.Start`` 与 ``Downloader.RunRecovery``：

- recovery 启动即执行，此后每 15s 有界读取 128 条 admission；同时接管
  legacy pending papers 的 adoption，schema 尚在迁移时下轮重试。
- fleet 模式关闭独立的 ``ingester.RecoverPending``，避免两个恢复所有者
  同时重放旧论文；非 fleet 模式保持原 ingest recovery。
- fleet maintenance 每 5s 调度，处理 lease expiry、staged 重试、hooks
  与 retention；每轮有自己的上下文预算。
- 终止时先取消并 join recovery/maintenance，再关闭 downloader 的
  admission 门、取消其生命周期上下文并 join producers/workers；若
  30s shutdown 等待超时，继续 join 后才释放池和 data lock。已接受的
  journal 工作留给下次启动，不因 shutdown 被永久标失败。

生产接线使用同一 PostgreSQL DB 的独立 fleet pool，最大连接数为
``max(16, min(64, 2*max_in_flight+8))``（默认 20），不同于 registry pool。
archive callback 持有 fleet task transaction 时需要 registry 连接，
分池避免等待自己的连接。迁移 ``00004_downloadfleet.sql`` 与
``00005_download_requests.sql`` 分别提供 fleet 与 admission 状态；启动
遵循现有异步 schema manager。当前 qatlasd 是**单 master 部署**，不能因
backend 有 SQL 锁就宣称多 master HA；底层多协调器还要求一致路径的共享
持久 spool，不能将各副本本地磁盘当成分布式 blob 存储。

claim、lease 与流式归档协议
--------------------------------------------------------------------------------

不同论文并行，同一 task 顺序尝试不同 worker。claim 事务使用 PostgreSQL
advisory scheduler lock，task/attempt 行锁与 ``current_attempt`` fencing
约束状态迁移。只允许 approved 且 heartbeat 足够新的节点领新任务；
draining 可交回既有结果但 claim 为空。并发额度同时取全局剩余额度、
master 单节点硬上限、worker 广告 capacity 与请求 limit 的最小值。
``running``、``uploading``、``staged`` 都占额度；已试过的 worker 不再
领取同一 task。可重试失败转回 queued，``invalid_identifier`` /
``cancelled``、预算耗尽或 task deadline 到达则终止。

执行 lease 默认 60s，heartbeat 续期受 attempt 默认 6m 与 task 默认
15m deadline 双重限制；task 预算从远端入队起算，不是最初 admission
起算。过期 lease 不会被复活。heartbeat response
中的 ``lease_expires`` 未包含的活跃下载应被 worker 停止。

上传是独立阶段，不是把整个 PDF JSON/base64 塞进控制请求：

1. worker 先将 PDF 写入本地文件、hash、fsync，再原子持久化为 ready。
   交付前查询 receipt；需要上传时用文件 reader 发送原始 PDF。
2. 第一次上传必须在有效 execution lease 内开始；master 将其转成
   **固定 2m transfer lease**，仍受 task deadline 限制，不受已结束的
   worker execution deadline 限制。重传使用同一个 attempt、摘要和大小，
   但新 generation 的 spool reservation；不能延长首次 transfer deadline。
3. master 先事务预留声明的字节，再流入 exclusive 0600 staging 文件，
   核对大小/摘要/PDF，fsync 文件及目录后提交 staged。慢传输持有
   task/attempt 行锁，不持有 scheduler/quota advisory lock；uploading
   heartbeat 只读取固定 lease，避免阻塞其他下载的续租。
4. ``commitStaged`` 打开文件，重新计算摘要、验证并 rewind；同步把
   **file-backed reader** 传给 ``ArchiveRemote``，不分配 PDF 大小的
   内存缓冲。callback 解析 canonical identity、写对象存储、注册 asset
   并完成相应 admission。任一步失败保留 staged，maintenance 重试。
5. archive callback 成功后，fleet 事务才把 attempt/task 设为 done 并
   写 outcome 与 hook outbox；此时才有可持久查询的 archive ACK。
   staged 不再被分配给其他 worker，允许执行 deadline 后继续归档。

没有对象存储与 PostgreSQL 的分布式事务：外部写成功、fleet commit
之前崩溃可重放 callback，所以 archive 与 hooks 都必须幂等。HTTP
200/202 或 ``staged`` 不能单独当作成功。worker 在 TTL 前只有收到
同时匹配 task ID、attempt ID、SHA-256、size 的 ``done`` receipt
才删除 ready PDF；receipt GET 不消费结果，丢失响应通过查询/重传恢复。

**转换 reconciliation 在 receipt 之后。** qatlasd 的
``durableRemoteHooks`` 对 DOI 调 ``EnsureByDOI``，对 arXiv 调 ``Ensure``；
MinerU 启用时，只有转换返回 ``JobStateDone`` 才继续后续 PDF-ready/index
hook。仅排入转换器内存队列不算 outbox 已交付，重启后会重新 Ensure。
显式禁用转换则跳过此等待。每次 hook 有 30s 预算，以有界指数退避最多
尝试 20 次；耗尽保留错误诊断。转换和索引失败不撤销 archive receipt，
也不让 worker 为 MinerU 保留已确认文件；外部副作用是 at-least-once，
不是 exactly-once。

保留期、快照与 worker 网络边界
------------------------------

master 默认 7d 保留 staged 恢复机会及可清理的终态 task/receipt 历史，
不是 archive ACK 后继续保留 staging PDF 七天：成功 staging 文件可立即
清理。长期无法归档的 staged task 超 retention 后失败清理；终态行需
hook 不再 pending、spool 文件清理后才可删除，receipt 随 task retention。
admission 终态记录也按 7d 分批清理，queued 不因此删除。

worker 默认 **ready 起算 24h**，与 master 7d 独立：到期先持久标记 expired，
再删 PDF、重试报告 timeout；正在上传的文件受保护。主机离线超过 TTL
不保证能归档。重启校验 ready 文件大小/摘要并恢复交付，先前 running
标为 interrupted failure；ready 丢失或损坏会拒绝启动而非假定成功。
每个活跃下载预留 100 MiB，结合 ready 占用、其他 reservation、实际剩余
磁盘与 metadata 余量限制 claim；不为腾空间驱逐未到期 ready 文件。
PDF quota 不包含 identity/catalog/browser profile 等开销。失败/expired
catalog 记录等待确认后移除，不应把 PDF TTL 理解为所有 metadata 的 TTL。

可见历史是有界快照而非完整分页历史：本地终态 progress 最多 200 条，
``/jobs`` 额外合并最多 512 条 durable queued admissions；admin snapshot
最多 500 workers 与 500 jobs。master 清理分批执行，failure trace 每
attempt 最多 64 条、成功 metadata header 上限 16 KiB；这些上限不是
积压任务的接收上限，也不表示所有 worker 本地 catalog 有固定条数上限。

worker 没有 HTTP listener，不需要映射端口；本地 CDP 仅绑定 loopback。
runner 独占持久 volume，identity 在注册前落盘（独立随机 ID 与 256-bit
secret），文件 0600 / 新目录 0700，volume lock 拒绝重复 runner；不能
复制同一 identity 并发运行，不能换 master URL 继续使用该 volume。
master 仅存 enrollment 与 worker credential 的哈希。网络分两类：

- master client 要求 HTTPS（``--allow-http`` 仅可信开发显式开启），
  拒绝所有 redirect，包含同 host redirect，避免 bearer 转发；正常 TLS
  校验不关闭。可使用进程 HTTP(S) proxy 环境、可访问私有 HTTPS master。
- PDF/landing/arXiv HTTP client 检查 URL 与全部 DNS answers，拒绝
  userinfo、非 HTTP(S)、私有/loopback/link-local/保留地址及混合
  public/private 解析，dial 固定的已验证 IP；redirect 重复检查，且
  绕过环境 HTTP proxies。这也可能拒绝校园私网 PDF 主机，无不安全绕过旗标。
- **这不是完整 SSRF/browser sandbox**：Chromium 导航、JS subresources、
  metadata resolver 是独立网络路径。worker/browser 必须通过隔离网络
  namespace 与 egress firewall 阻止访问 host 服务、内网、cloud metadata
  和敏感 socket，明确放行必要 master/CDP 端点；不要用 host networking
  或挂载 Docker/database socket。外置 CDP 需要相同隔离。

HTTP 控制面与独立凭据
--------------------------------------------------------------------------------

普通用户与管理员 API 不接受 worker secret 代替用户身份。管理员生成
默认 15m 的一次性 enrollment credential；worker 注册 pending 后必须
由人类管理员批准，不能自批准。丢失注册响应可用同一个已持久身份重试，
已消费 enrollment 不能注册另一身份。pending 仅可查询 status；
reject/revoke 是终止状态，需要新身份重新 enrollment，revoke 使鉴权失效。
管理 UI 位于 ``/{en|zh}/admin/downloader-workers``。

.. list-table::
   :header-rows: 1
   :widths: 42 20 38

   * - qatlasd API
     - 鉴权
     - 行为
   * - ``POST /api/downloader/fetch``
     - ``papers:write``
     - ``{"items": [...]}``，最多 50 行；逐项解析、ResolveOrMint、持久
       admission/入队；返回 items/enqueued，逐项报告接收失败。
   * - ``GET /api/downloader/jobs``
     - ``papers:read``
     - 本地 progress + durable queued 合并快照、进程计数器；禁用时空快照
       200（fetch 禁用时 503），不是两个端点都 503。
   * - ``GET /api/downloader/remote-jobs``
     - ``papers:read``
     - durable jobs（含 worker_id，但不返回 node 列表/凭据）；禁用返回
       ``enabled:false`` 与空 jobs。
   * - ``GET /api/admin/downloader/workers``
     - admin session
     - 有界 workers/jobs snapshot；不暴露密钥。
   * - ``POST /api/admin/downloader/enrollment``
     - admin session
     - 创建一次性 enrollment token 与 expires_at，201。
   * - ``POST /api/admin/downloader/workers/{id}/{action}``
     - admin session
     - ``approve`` / ``reject`` / ``drain`` / ``enable`` / ``revoke``；
       enable 只恢复 draining，不能复活 rejected/revoked。

adminGuard 在 sessionGuard 上加管理员 allowlist；PAT（包括 system PAT）
不代替管理员 session。v2 handler 独立自鉴权，不要再套用户 scope/admin
bearer guard。下表路径均相对 ``/api/downloader/workers/v2``：

.. list-table::
   :header-rows: 1
   :widths: 30 70

   * - v2 API
     - 请求 / 结果
   * - ``POST /register``
     - JSON ``id,name,enrollment_token,secret`` → Node；注册后只使用该
       worker 的 ``Authorization: Bearer <secret>``。
   * - ``GET /status``
     - Node，包括 pending/approved/draining 状态；pending 可调用。
   * - ``POST /heartbeat``
     - capacity、browser/disk/spool 状态、running_attempt_ids → status
       与 lease_expires map；不能无限延长 upload lease。
   * - ``POST /claim``
     - limit → attempts（task_id、attempt_id、ref、lease_expires、
       deadline、max_pdf_bytes）；空列表正常。
   * - ``POST /report``
     - attempt_id、failure、error、trace → Receipt，失败可能调度下个节点。
   * - ``PUT /upload/{attempt_id}``
     - ``application/pdf`` 原始字节；headers 为 ``X-PDF-SHA256`` 与
       ``X-PDF-Size``；可带 ``X-Source-URL``、``X-Download-Strategy`` 与
       ``X-Download-Result`` （无 padding 的 base64url JSON metadata）。
   * - ``GET /receipts/{attempt_id}``
     - 非破坏性读取本 worker 的 Receipt：running/staged/done/failed/expired；
       内部 uploading 对外映射为 running。

控制 JSON 上限 64 KiB；上传路由先移除 PocketBase rereadable body wrapper，
避免重读缓存整个 PDF，再覆盖其默认 32 MiB body limit 为 100 MiB，fleet
还独立限流量/读取 deadline。反向代理需透传 Authorization、上述全部
metadata headers，允许 100 MiB PDF 与 transfer 时间，并保护自己的
upload buffering 目录。worker client 控制请求总预算 20s、upload 3m，
失败通常按 10s cadence 重试；**client 3m 不会延长 master 固定 2m lease**。

当前配置默认值
--------------

下表键相对 ``config.yaml`` 的 ``downloader:``；这里只列当前已接线的
YAML 键，不把 Go embedding 参数冒充配置项。``remote.enabled`` 需要
``paper_access.enabled``、``downloader.enabled``、``postgres.dsn``
与已有对象存储；和非空 ``proxy.url`` 同时配置会报错。

.. list-table::
   :header-rows: 1
   :widths: 34 20 46

   * - YAML 键
     - 默认
     - 含义
   * - ``enabled`` / ``concurrency``
     - true / 2
     - 下载器开关 / 本地 fetch slots；仍受默认关闭的 paper_access 总开关约束。
   * - ``unpaywall_email`` / ``s2_api_key``
     - 空
     - OA 联系方式/凭据；Unpaywall email 回落 OpenAlex mailto。
   * - ``respect_robots``
     - false
     - 可选 robots 门，不是权限证明或网络 sandbox。
   * - ``browser.cdp_url`` / ``browser.timeout``
     - 空 / 45s
     - 本地 browser lane 端点与预算。
   * - ``agent.backend``
     - 空（关闭）
     - ``openai`` / ``claude``，其余 backend 参数沿用现有实现。
   * - ``remote.enabled``
     - false
     - 启用 outbound fleet 与 admission journal。
   * - ``remote.max_in_flight``
     - 6
     - fleet 全局并发；也作为额外本地 remote waiters 配额。
   * - ``remote.max_worker_in_flight``
     - 2
     - master 单 worker 硬并发上限。
   * - ``remote.max_worker_attempts``
     - 3
     - 每 task 不同 worker 尝试数；安全上限 32。
   * - ``remote.task_timeout`` / ``remote.worker_timeout``
     - 15m / 6m
     - task 与单次执行预算；安全上限 24h / 1h。
   * - ``remote.lease_duration``
     - 60s
     - 执行续租窗口；YAML 要求至少 15s，安全上限 5m，且
       lease ≤ worker timeout ≤ task timeout。
   * - ``remote.spool_dir``
     - ``<pb_data_dir>/downloader-spool``
     - 持久 master staging 路径。
   * - ``remote.spool_max_bytes``
     - 2147483648（2 GiB）
     - master staging quota，至少 100 MiB。
   * - ``proxy.url`` / ``proxy.token`` / ``proxy.timeout``
     - 空 / 空 / 5m
     - 仅 legacy 入站代理客户端，和 remote 互斥。

``downloadfleet.Config`` 另有 Go 层 ``UploadTimeout=2m``（上限 10m）、
``EnrollmentTTL=15m``、``Retention=7d``、``MaxPDFBytes=100 MiB``；
qatlasd 当前没有对应 YAML override。直接嵌入包而不使用 main 接线时，
spool 默认是临时目录 ``qatlas-downloadfleet``、1 GiB，不能误当作
qatlasd 的持久目录/2 GiB 默认。

.. list-table::
   :header-rows: 1
   :widths: 40 24 36

   * - worker 环境变量 / flag
     - 默认
     - 含义
   * - ``DL_WORKER_MASTER_URL`` / ``--master-url``
     - 必填
     - 最终 HTTPS master origin，可含 reverse-proxy path prefix。
   * - ``DL_WORKER_ENROLLMENT_TOKEN``
     - 首次注册必填
     - env-only；注册成功后可移除，不是长期 worker credential。
   * - ``DL_WORKER_NAME`` / ``--name``
     - hostname
     - 运维显示名。
   * - ``DL_WORKER_DATA_DIR`` / ``--data-dir``
     - ``/var/lib/qatlas-downloader``
     - 每 worker 独占持久 volume。
   * - ``DL_WORKER_CONCURRENCY`` / ``--concurrency``
     - 2
     - 范围 1–32；受 master 硬上限约束。
   * - ``DL_WORKER_MAX_SPOOL_BYTES`` / ``--max-spool-bytes``
     - 10737418240（10 GiB）
     - PDF quota，至少 100 MiB；另留 catalog/browser 磁盘空间。
   * - ``DL_WORKER_RESULT_TTL`` / ``--result-ttl``
     - 24h
     - ready PDF 保留期，正数 Go duration。
   * - ``DL_BROWSER_CDP_URL`` / ``--browser-cdp-url``
     - 空
     - 外部 CDP；设置后不启动本地浏览器。
   * - ``DL_BROWSER_BINARY`` / ``--browser-binary``
     - 自动检测
     - 镜像指定 headless-shell；runner 是本地浏览器的唯一所有者。
   * - ``DL_UNPAYWALL_EMAIL`` / ``--unpaywall-email``、``DL_S2_API_KEY``
     - 空
     - OA 联系方式 / env-only S2 key。
   * - ``--task-timeout`` / ``--allow-http``
     - 6m / false
     - 本地任务预算（还受 assignment/lease 限制）/ 仅可信开发 HTTP。
   * - ``--healthcheck``
     - 检查后退出
     - 读取本地 health.json，不监听端口；pending 尚未批准会 unhealthy。

legacy downloaderproxy（保留兼容，不是 v2 worker）
--------------------------------------------------------------------------------

``cmd/downloaderproxy`` / ``Dockerfile.downloaderproxy`` 仍是 master 主动
连接单个入站服务的旧方案：``RemoteProxy`` submit → poll → 一次性 token
GET 文件，默认客户端总预算 5m、轮询 1.5s。它没有 fleet 的 enrollment、
多节点 claim、durable admission/receipt 或 archive ACK 语义。

当前 ``/healthz`` **不鉴权**，只回 ``{"status":"ok"}``，不是 browser
健康证明；仅 ``/v1/jobs``、``/v1/jobs/{id}``、``/v1/files/{token}`` 在
设置 ``DL_PROXY_TOKEN`` 时要求 Bearer。file 响应实际只提供
``X-Qatlas-Sha256``，不提供旧文档写过的 ``X-Proxy-Source-Url``。
token 在成功完成后 30m 到期且 GET 时即消费；传输失败不保证可重取。
过期清理与 job map 删除有额外延迟，并非统一的“完成 30m 删文件、1h
删所有终态”；不要依赖它作持久历史或恢复队列。旧浏览器监督逻辑不在
这里作健壮性承诺，也不把 ``/healthz`` 当其验收结果。

包结构与接线入口
----------------

.. list-table::
   :header-rows: 1
   :widths: 38 62

   * - 路径
     - 职责
   * - ``internal/downloader/``
     - ``downloader.go`` 策略/队列/存储；``remote.go`` 委派与 archive hook；
       ``journal.go`` admission/recovery；``progress.go`` 快照；
       ``fetcher.go`` / ``validate_reader.go`` 候选及现有对象验证；
       resolvers/patterns/landing/browser/agent/claude 保留策略实现。
   * - ``internal/downloadfleet/``
     - service/admissions 管 task generation；workers 管 approval/claim/lease；
       http/upload/transfer 管 wire 与 staging；maintenance 管恢复/outbox/清理。
   * - ``internal/downloadworker/``、``cmd/downloaderworker/``
     - outbound client/runner、持久 identity/spool、browser、public HTTP guard；
       worker 镜像 entrypoint 仅 exec runner，不再启动第二个浏览器。
   * - ``internal/workerprotocol/protocol.go``
     - v2 精确路由、header、assignment/result/receipt wire 类型。
   * - ``internal/registry/``
     - fleet/admission migrations、download_requests 与 legacy pending adoption。
   * - ``internal/routes/downloader.go`` / ``downloadfleet.go``
     - 用户提交/快照、独立 worker 鉴权、admin session 面及流式 body 接线。
   * - ``cmd/qatlasd/main.go``、``download_lifecycle.go``、``download_hooks.go``
     - 独立池/锁/生命周期所有权、archive callback 与 receipt 后转换 reconciliation。
   * - ``internal/downloader/proxy.go``、``cmd/downloaderproxy/``
     - legacy 入站代理，独立于 v2 协议。

用户第三方搜索 key（``internal/userkeys``）
-------------------------------------------

multi / agentic 搜索按用户注入第三方 key（IEEE、Scopus、Tavily…），
key 的存放与解密全在 qatlasd：

- 存储：PocketBase 集合 ``search_api_keys``\（迁移
  ``1791000000``；``user`` 关联级联删除、``backend`` 名、
  ``key_encrypted`` 隐藏字段；``UNIQUE(user, backend)``）——
  owner-only 访问规则、**只允许服务端 handler 写入**\（与
  ``pat_tokens`` 同构）；
- 加密：AES-256-GCM，密钥为 system PAT 的域分离 SHA-256
  （``qatlas-userkeys-v1:`` 前缀）；无新增配置键，但**轮换 system
  PAT 会使存量 key 失效**\（解密失败跳过并告警，用户重新录入）；
  单独泄露 pb_data 不泄露 key 明文；
- HTTP 面（``internal/routes/me_search_keys.go``）：
  ``GET/PUT/DELETE /api/me/search-keys``，**sessionGuard** 而非
  scopeGuard——泄露的 PAT 不得读取或替换所有者的第三方 key；
  列表只回掩码提示（末 4 位）；
- 代理面（``internal/routes/search_multi.go``）：
  ``POST /api/search/multi`` 把调用者的 key 解密后以 ``api_keys``
  转发给 qatlas-search 微服务（微服务本身从不持久化）；
  ``GET /api/search/backends`` 合并静态目录、微服务实时可用性与
  用户已存 key，驱动 SPA 的 backend 勾选框。
