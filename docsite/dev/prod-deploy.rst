生产部署手册（线上实例）
==============================

:doc:`release` 描述"代码如何变成产物"的标准化流程，本文描述**产物如何在
线上实例落地**：部署账号拓扑、目录与网络布局、日常运维动作，以及
qatlas-rag 与多 downloader-worker 的首次启用 runbook，以及旧 downloaderproxy
的兼容说明。账号与网络布局沿用原实例的记录；新增 worker 部分是部署步骤，
**不表示现有线上实例已经升级**。后续环境请替换具体账号、域名和路径。

部署账号拓扑
------------

线上主机上每个栈由**独立的无登录密码系统账号**持有，仅通过 SSH 公钥
登录，且只加入 ``docker`` 用户组（docker 组等价于 root，不再额外授予
sudo）：

.. list-table::
   :header-rows: 1
   :widths: 26 34 40

   * - 账号
     - 持有资产
     - 说明
   * - ``qatlas-admin``
     - qatlas 应用栈（qatlasd / qatlas-search / 将来的 qatlas-rag）
     - compose 项目 ``deploy``、代码 checkout ``~/projects/qatlas-dev``、
       配置 ``~/.qatlas/``、ghcr 拉取凭据
   * - ``postgres-server-admin``
     - 共享 PostgreSQL 容器
     - compose 项目 ``postgres``（``~/postgres/docker-compose.yml``），
       声明式接管原裸 ``docker run`` 容器；数据卷 ``postgres-data`` 以
       ``external: true`` 引用
   * - 个人账号（如 agony）
     - 仅开发与发版
     - 发版推 tag 在个人账号的 checkout 进行；**不**\ 在服务器上保存
       个人全权限 PAT

registry 凭据使用**最小权限原则**：部署账号的 ``docker login ghcr.io``
使用只带 ``read:packages`` 单一 scope 的 PAT（GitHub 没有 org 级 PAT，
多人共用部署时可在 org 内设机器账号持有该 PAT）。个人开发用的全权限
PAT 只允许出现在开发机，不出现在部署机。

目录与配置布局
--------------

qatlasd 拒绝一切环境变量配置，配置全部来自 YAML 文件；compose 模板以
``${HOME}`` 和相对路径引用一切资源，因此同一份
``deploy/docker-compose.yml`` 在任何账号下都无需修改：

.. code-block:: text

   qatlas-admin:
     ~/projects/qatlas-dev/           # 四个仓库的 checkout（部署只用 QuantumAtlas/deploy）
     ~/.qatlas/config.yaml            # qatlasd 配置（唯一来源，只读挂载）
     ~/.qatlas/search.yaml            # qatlas-search 配置（读写挂载，admin 端点回写）
     ~/.qatlas/rag.yaml               # qatlas-rag 配置（本机启用 rag 时挂载）
     ~/.qatlas/docs/{doc,devdoc}      # 文档覆盖目录（见 release.rst"文档的独立更新"）

   postgres-server-admin:
     ~/postgres/docker-compose.yml    # postgres:18.4 + external 卷/网络
     ~/postgres/.env                  # POSTGRES_PASSWORD（600）

两个配置文件约定：含密钥的文件对容器内 distroless nonroot 用户
（UID 65532）而言是 "other"，需要 ``o+r``\ （如 644）；这是容器读取
宿主机 bind mount 的既定做法，主机层面靠账号隔离保密。

网络拓扑
--------

容器间一律用**容器名 + 用户自定义网络的 DNS** 寻址，不写死 IP：

.. list-table::
   :header-rows: 1
   :widths: 22 78

   * - 网络
     - 成员与用途
   * - ``shared-infra``\ （external）
     - ``postgres`` / ``qatlasd`` / ``qatlas-search`` /（可选）``qatlas-rag``。
       qatlasd 的 ``postgres.dsn`` 指向 ``postgres:5432``；app 微服务之间
       的服务 token 寻址也走这里
   * - ``deploy_default``\ （compose 项目内建）
     - 与 qatlasd 同栈的旁路容器（如 qdotdb-webui）也经此网络访问
       ``postgres``，因此 postgres 的 compose 声明把它列为 external
       一并加入，**不得删除该网络**

对外暴露只有两个入口：Caddy（systemd）反代 ``127.0.0.1:4200``，以及
PostgreSQL 绑定 ``127.0.0.1:5432``。app 微服务一律不映射宿主机端口。

日常运维
--------

**版本升级**\ （顺序永远先下游后上游：qatlas-rag → qatlas-search →
qatlasd，原理见 :doc:`release`）：

.. code-block:: bash

   ssh qatlas-admin@<host>
   cd ~/projects/qatlas-dev/QuantumAtlas/deploy
   $EDITOR .env        # 修改 QATLAS_VERSION / QATLAS_SEARCH_VERSION / QATLAS_RAG_VERSION
   docker compose --profile search pull
   docker compose --profile search up -d
   curl -s http://127.0.0.1:4200/api/health | jq .data.version

**文档更新**：``./deploy/update-docs.sh``\ （qatlasd 无感，无需重启）。

**备份**：数据库 ``docker exec postgres pg_dumpall -U postgres | gzip``；
PocketBase 数据 ``tar czf pb_data.tgz -C <repo>/QuantumAtlas data``。

qatlas-rag 首次启用 runbook
---------------------------

rag 链路涉及三个仓库的协调发版，首次启用按以下顺序执行：

.. list-table::
   :header-rows: 1
   :widths: 30 70

   * - 前提
     - 验收
   * - qatlas-rag ≥ ``v0.1.0``
     - ghcr 存在 ``qatlas-rag:v0.1.0`` 镜像
   * - qatlas-search ≥ ``v0.2.0``
     - 含 ``rag`` backend（``backends/rag.py``）
   * - qatlasd ≥ ``v0.23.0``
     - 支持 ``rag.remote`` 配置段，插件注册表出现 ``rag-remote``
   * - GPU 主机
     - NVIDIA GPU + ``nvidia-container-toolkit``\ （bge-m3 + reranker
       不支持 CPU 运行）；qatlas-rag 与 qatlasd 可不同机，经网络互通即可

1. **GPU 主机部署 qatlas-rag**：用 qatlas-rag 仓库
   ``deploy/docker-compose.example.yaml``\ 起服务；模型权重缓存用
   ``hf-cache`` named volume——**首次只建空缓存**，权重由容器首次启动
   时自行下载填充（离线环境需预先 warm，见仓库 README）。
2. **生成 service token**：``openssl rand -hex 32``，三处保持一致——
   qatlas-rag 配置 ``rag.service_token``、qatlasd 的
   ``rag.remote.token``、qatlas-search 的 ``search.rag.token``。
3. **配置 qatlasd**\ （``~/.qatlas/config.yaml``）::

      rag:
        remote: { enabled: true, url: "http://<rag-host>:8801", token: "<token>" }

4. **配置 qatlas-search**\ （``~/.qatlas/search.yaml``）::

      search:
        rag: { enabled: true, url: "http://<rag-host>:8801", token: "<token>" }

5. **滚动生效并验证**：重启 qatlasd 与 qatlas-search（配置段在启动时
   读取）；管理员页面插件列表中 ``rag-remote`` 应为 connected；把一篇
   论文置为 ready 后观察 qatlas-rag 日志出现 ``POST /v1/index``；
   最后做一次语义搜索冒烟。

**回退**：任一环异常时把对应配置段改回 ``enabled: false``\ 并重启该
服务即可，索引推送是 best-effort，失败不会阻塞主流程；rag 的缺失只让
语义检索路径降级，不影响关键词检索。

多 downloader-worker 首次启用 runbook
------------------------------------------------------------------------

新部署使用 ``cmd/downloaderworker``。调度器内置在 qatlasd 中，worker
主动向主服务注册、心跳、领取任务并上传 PDF；无需主服务反向访问电脑，
也不需要新增共享磁盘、Redis 或消息中间件。实现与状态机见 :doc:`downloader`。

前提与持久目录
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

- 主服务运行**包含本次 worker 改造的版本/提交**，已有 PostgreSQL 与对象
  存储配置，且 ``paper_access.enabled`` / ``downloader.enabled`` 均开启。
- worker 能出站访问主服务的最终 HTTPS 地址；不要依靠 HTTP 跳转到 HTTPS，
  runner 拒绝重定向。需要访问出版社的合法订阅或开放获取来源。
- 主服务默认暂存目录是 ``<paths.pb_data_dir>/downloader-spool``，应随
  ``pb_data`` 持久化；自定义 ``spool_dir`` 则另外挂载可写数据卷。
- 每个 worker 使用一个独立持久卷，保存身份凭证、任务记录与待交付 PDF；
  默认容器路径 ``/var/lib/qatlas-downloader``。不得复制身份给同时运行的
  另一台电脑，也不能只放在容器临时文件系统中。

主服务启用
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

在现有 ``config.yaml`` 中加入以下段落；若已有 ``downloader:``，应合并
而不是写重复 YAML 键：

.. code-block:: yaml

   paper_access:
     enabled: true
   downloader:
     enabled: true
     concurrency: 2
     remote:
       enabled: true
       max_in_flight: 6
       max_worker_in_flight: 2
       max_worker_attempts: 3
       task_timeout: 15m
       worker_timeout: 6m
       lease_duration: 60s
       spool_max_bytes: 2147483648
       # spool_dir: /srv/qatlas/downloader-spool

``remote.enabled: true`` 不能与非空 ``downloader.proxy.url`` 共存；先备份旧
配置，再移除旧 URL。重启主服务后，等待既有的异步 schema 管理器完成
``00004_downloadfleet.sql`` 和 ``00005_download_requests.sql``，再注册
节点。不要只复制新 YAML 到不认识这些字段的旧二进制上。

fleet 使用同一 PostgreSQL 的独立连接池，默认最多 20 个连接（按并发配置
计算，范围 16–64），**另加** 现有 registry 连接池；部署前检查数据库连接预算。
本期支持单主服务，不应据此假定已具备多主服务 HA。

worker 安装、注册与审批
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

1. 在受控构建机或 worker 主机，从与主服务匹配的 checkout 构建镜像。
   当前未新增 worker 镜像的 CI 发布通道，不要假定某个 ghcr tag 已存在：

   .. code-block:: bash

      docker build -f Dockerfile.downloaderworker -t qatlas-downloaderworker:<revision> .
      docker volume create qatlas-worker-a-data

2. 主服务管理员打开 ``/zh/admin/downloader-workers``（英文路径对应
   ``/en/admin/downloader-workers``），生成一次性注册令牌，默认 15 分钟过期。
   **建议镜像构建完成后再生成令牌**，以免构建期间过期。
3. 在 worker 电脑创建仅部署账号可读的 ``worker.env`` （例如权限 600）：

   .. code-block:: text

      DL_WORKER_MASTER_URL=https://qatlas.example.org
      DL_WORKER_ENROLLMENT_TOKEN=<new-enrollment-token>
      DL_WORKER_NAME=campus-a
      DL_WORKER_DATA_DIR=/var/lib/qatlas-downloader
      DL_WORKER_CONCURRENCY=2
      DL_WORKER_MAX_SPOOL_BYTES=10737418240
      DL_WORKER_RESULT_TTL=24h
      DL_UNPAYWALL_EMAIL=<contact@example.edu>
      DL_S2_API_KEY=<optional-semantic-scholar-key>

   不使用某项可选凭据时删除该行，不能把尖括号占位符作为实际密钥。主服务
   ``qatlasd`` 仍从 YAML 读配置；这里的环境变量只用于独立 worker。
4. 启动节点，不发布入站端口：

   .. code-block:: bash

      docker run -d --name qatlas-worker-a --restart unless-stopped \
        --stop-timeout 30 --shm-size 256m \
        --env-file worker.env \
        -v qatlas-worker-a-data:/var/lib/qatlas-downloader \
        qatlas-downloaderworker:<revision>

   镜像以 UID/GID 65532 运行。named volume 继承镜像目录权限；若使用 bind
   mount，请预先使目录对该 UID 可写，不能靠全局开放读写权限解决。
5. 管理页出现 ``pending`` 后核实节点身份并批准。批准前只能查询状态，不能
   领取或上传任务。注册成功后身份和独立节点密钥已保存于卷中，可以在下次
   重启时移除注册令牌环境变量；正常重连无需重新审批。
6. 为电脑 B 生成**新注册令牌和新数据卷**重复上述步骤。所有电脑只配置主服务
   地址，主服务没有需要逐台维护的 worker URL 列表。

浏览器、网络与反向代理
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Go runner 单独负责本地浏览器启动与监督；entrypoint 只 exec runner，避免
两处重复启动。CDP 仅绑定容器内 ``127.0.0.1:9222``，不要映射该端口。
可用 ``DL_BROWSER_CDP_URL`` 指向受信、外部维护的 CDP，此时 runner 不负责
启动或终止外部浏览器。浏览器就绪不代表具有所有出版社的订阅权限。

普通 PDF/落地页与 arXiv HTTP 获取会验证公网地址并固定 DNS 解析结果拨号，
绕过环境 HTTP 代理；主服务通信及元数据解析使用不同客户端，浏览器/JS
子资源也不由该 HTTP 校验覆盖。应使用网络命名空间和出站防火墙隔离敏感
内网、宿主服务与云元数据端点，仅放行必要的主服务/CDP 等受信地址；不要用
host 网络，也不要挂载 Docker socket 或数据库凭据。校园内私网 PDF 主机
可能被普通获取通道拒绝，不能据此声称整个浏览器已有完整 SSRF 沙箱。

主服务前的 HTTPS 反代需允许不超过 **100 MiB** 的 PDF 请求、保留
``Authorization``、``X-PDF-*``、``X-Download-*`` 和 ``X-Source-URL``
头，并给上传足够时限。主服务上传阶段默认固定预算 2 分钟、worker 客户端
传输超时 3 分钟，两者都不是注册控制请求的超时。PocketBase 的 32 MiB 默认
限制在该上传路由上已覆盖，并关闭 PDF body 的重读缓存以保持流式接收。

归档、清理与故障处理
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

- worker 下载完成只表示本机 ``ready``；它会主动上传并查询交付回执。
- 主服务校验文件、写对象存储、登记 registry 后才返回持久 ``done`` 回执。
  worker 需核对 task/attempt ID、SHA-256 与大小，再删除本机副本；收到普通
  HTTP 200 或 ``staged`` 不能删除。MinerU 后续处理不阻塞这个归档确认。
- 上传中断或确认丢失先重传/查回执，不重新抓取论文。主服务重启恢复任务与
  归档；worker 重启恢复已下载文件，执行中任务按 interrupted 处理。
- worker 未交付结果默认保留 24 小时；主服务 ``staged`` 归档重试默认保留
  7 天。长期离线可能超过保留期限；磁盘不足时停止接新任务，不驱逐仍在
  有效期内的结果。两侧暂存目录与数据库都应纳入容量监控和备份考虑。
- ``drain`` 停止新任务、允许旧任务交付；``enable`` 只恢复排空节点；
  ``reject`` / ``revoke`` 为终态，重新接入需要新身份。撤销前若希望保留
  正常交付能力，应先排空，不能直接丢弃旧数据卷。

验收与升级回退
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

先验证单节点完整流程，再测试两个节点同时处理不同论文，并让一个节点
停止接单或断线以确认不会继续向其分配任务。通过主服务 Downloader 页面
或 ``POST /api/downloader/fetch`` 提交有授权的样例，检查
``GET /api/downloader/remote-jobs`` 中的 worker、任务状态，以及归档后主服务
资产可读、worker 暂存已清理。若本地已成功或已有 PDF，根本不会生成远端任务；
需选择确实会进入远端流程的样例。不要将 ``probe --proxy`` 当作新池验收。

离线自动化测试可使用合成 PDF 和**独立测试数据库**：

.. code-block:: bash

   go test ./...
   TEST_DOWNLOADFLEET_DATABASE_URL='postgres://test_user:password@localhost/test_database?sslmode=disable' \
     go test -race ./internal/downloadfleet ./internal/downloadworker ./internal/registry

测试会创建并清理自己的 schema，不得指向生产数据库。真实 Chrome、容器运行
和机构网络权限仍需在受控预发布环境验收；本 runbook 不声称这些已在线上完成。

升级 worker 前先排空并等待交付，停止后用新镜像重建容器，**复用原数据卷**。
主服务与 worker 采用同一实现版本或经验证的 v2 协议组合。首次迁移先保留旧
proxy，确认新池稳定后再退役。

回退路由时保留**新版主服务二进制**，将 ``remote.enabled`` 设为 false，
必要时恢复旧 ``proxy.url/token`` 并重启。保留数据库及两侧暂存卷，便于恢复。
不能直接回退旧二进制：既有 schema-version guard 会拒绝新 schema，而向下
迁移会删除 fleet/admission 记录。回退步骤与常规 app 的下游优先升级不是同一件事。

旧 downloaderproxy 部署（兼容路径）
------------------------------------------------------------------------

downloaderproxy 是健壮下载器（见 :doc:`downloader`）在校园出口机器上的
独立部署形态：它的出口带机构订阅，qatlasd 自身的代理网络打不通的
出版社内容委派给它取。它**不在 ghcr、不在 qatlasd 的 compose 栈内**，
是在 campus-egress 主机上现场构建并运行的旧协议组件（分发边界见
:doc:`release`）。以下仅用于维护存量部署；新 worker 不使用此端口、共享
proxy token 或一次性领取协议。

.. list-table::
   :header-rows: 1
   :widths: 30 70

   * - 前提
     - 验收
   * - campus-egress 主机
     - 出口带机构订阅（直接访问出版社即 entitled）；装有 Docker，
       且 Docker **不配置** http_proxy/https_proxy（否则丢失校园
       出口身份）；与 qatlasd 主机网络互通（如 ``ag-workstation``
       可被 qatlasd 解析访问）
   * - QuantumAtlas checkout
     - 主仓 checkout（部署分支或目标 tag），供现场构建镜像
   * - qatlasd ≥ ``v0.27.0``
     - 支持 ``downloader.proxy`` 配置段；插件注册表出现
       ``downloader`` builtin

1. **生成共享 token**：``openssl rand -hex 32``，两处保持一致——
   容器环境变量 ``DL_PROXY_TOKEN`` 与 qatlasd 的
   ``downloader.proxy.token``。
2. **campus 主机构建并运行**\（在 QuantumAtlas checkout 内）::

      docker build -f Dockerfile.downloaderproxy -t qatlas-downloaderproxy .
      docker run -d --name downloader-proxy --restart unless-stopped \
        -p 8602:8602 \
        -e DL_PROXY_TOKEN=<token> \
        -e DL_UNPAYWALL_EMAIL=<contact@example.edu> \
        -e DL_S2_API_KEY=<s2k-...> \
        qatlas-downloaderproxy

   镜像自带 headless-shell；旧版 entrypoint 和 Go 服务均有浏览器启动
   逻辑，不能把服务存活等同于浏览器可用，也不保证通过所有 WAF。
   为保留机构出口身份，**不要**\ 注入 http_proxy/https_proxy 环境变量。
3. **配置 qatlasd**\（``~/.qatlas/config.yaml``）::

      downloader:
        proxy:
          url: "http://<campus-host>:8602"
          token: "<token>"

   重启 qatlasd（配置段在启动时读取）。
4. **验证**：

   - campus 主机 ``curl -s http://127.0.0.1:8602/healthz``\ 返回
     ``{"status":"ok"}``，只证明旧 HTTP 服务存活，不证明浏览器或订阅可用；
   - qatlasd 主机上现场压测委派链路：
     ``qatlasd downloader probe <一个此前失败的 DOI> --proxy
     http://<campus-host>:8602 --proxy-token <token>``\，确认
     winning strategy 为 ``remote-proxy:<s>``；
   - SPA 的 Robust Downloader 页面提交同一 DOI，任务 trace 中出现
     ``remote-proxy`` 尝试并成功。

**升级**：在 campus 主机的 checkout 内 ``git fetch && git checkout
<tag>`` 后重复第 2 步的 build + run（先 ``docker rm -f
downloader-proxy``）；它没有状态（文件 token 全在内存 /
``/tmp``），随主仓 tag 演进即可。

**回退**：qatlasd 侧删掉 ``downloader.proxy`` 段并重启——梯子回到
本地策略（含本地 browser lane），Robust Downloader 功能不中断，只是
失去 campus 出口这一跳；campus 主机容器可保留待用。
