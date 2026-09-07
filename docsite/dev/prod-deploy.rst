生产部署手册（线上实例）
==============================

:doc:`release` 描述"代码如何变成产物"的标准化流程，本文描述**产物如何在
线上实例落地**：部署账号拓扑、目录与网络布局、日常运维动作，以及
qatlas-rag 与 downloaderproxy 的首次启用 runbook。本文以当前唯一的生产实例为蓝本，后续新增
部署环境时按本文复制一份并替换具体值即可。

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

downloaderproxy 首次启用 runbook
--------------------------------

downloaderproxy 是健壮下载器（见 :doc:`downloader`）在校园出口机器上的
独立部署形态：它的出口带机构订阅，qatlasd 自身的代理网络打不通的
出版社内容委派给它取。它**不在 ghcr、不在 qatlasd 的 compose 栈内**，
是在 campus-egress 主机上现场构建并运行的唯一例外组件（理由见
:doc:`release`）。

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

   镜像自带 headless-shell（Chrome for Testing，过出版社 WAF 的
   指纹），entrypoint 起浏览器后由服务的监督器接管（CDP 死锁自动
   重启）。**不要**\ 注入任何代理相关环境变量。
3. **配置 qatlasd**\（``~/.qatlas/config.yaml``）::

      downloader:
        proxy:
          url: "http://<campus-host>:8602"
          token: "<token>"

   重启 qatlasd（配置段在启动时读取）。
4. **验证**：

   - campus 主机 ``curl -s http://127.0.0.1:8602/healthz``\ 返回
     ``{"status":"ok"}``；
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
