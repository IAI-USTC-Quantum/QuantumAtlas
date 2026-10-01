从搜索到可复现阅读
==================

先找到论文，再确认身份，最后选择读取内容或固定资产。``qa_…`` 标识一篇作品，
``source_id`` 固定 PDF 字节，``revision_id`` 固定解析结果；它们解决不同的问题。

准备客户端
----------

安装与服务端同一 ``major.minor`` 的客户端。运行中的服务版本可从
``/api/server/info`` 或 ``/api/health`` 查看，具体发行版本见
`CLI Releases <https://github.com/IAI-USTC-Quantum/qatlas-cli/releases>`_。
RC 须显式选择，普通升级命令可能保留较旧的稳定版：

.. code-block:: bash

   uv tool install 'qatlas-cli==0.37.0rc2'
   qatlas --version
   qatlas config set server_url https://qatlas.hfnl.app.chenzhaoyun.com
   qatlas auth login

已有客户端的用户使用 ``uv tool upgrade 'qatlas-cli==0.37.0rc2'``。
登录与 PAT 配置见 :doc:`cli`；已有配置和登录态保留。

查找与确认身份
--------------

``qatlas search`` 和 ``qatlas match`` 需要各自插件；安装方式见
:doc:`cli`。也可从 Web 搜索页选择论文，或调用 :doc:`api` 中的接口。

.. code-block:: bash

   qatlas search 'quantum linear systems' --json
   qatlas match 1010.4458 --json

搜索结果中的 ``errors`` 表示部分渠道失败，不能把失败后空结果解释为没有相关
论文。普通搜索在“有错误且无可用结果”时返回退出码 1；有可用的部分结果仍返回 0。
优先使用已锚定结果的 ``paper_id``，匹配接口本身只检查内部注册表。

读取 Markdown
--------------

将示例 ID 替换为实际返回的作品 ID：

.. code-block:: bash

   qatlas paper get metadata qa_01m0qv0k4kajjb41fwaemspcrd
   qatlas paper get markdown qa_01m0qv0k4kajjb41fwaemspcrd --max-wait 60 --request-timeout 20

``has_md: true`` 表示注册表记录了解析资产，不能证明存储此刻可读。
Markdown 缓存探测、打开与首字节读取共用至多 10 秒的存储读时限；更短的请求
deadline 优先。存储故障返回 503、``code=asset_store_unavailable``、
``retryable=true`` 和 ``Retry-After``，不启动替代下载或 MinerU 转换，
也不清空登记的资产。已开始传输后的故障会中断流，不能再改成 JSON 503。

真正的缓存未命中仍按原有接口返回 202 并轮询转换进度；转换失败与存储不可读
分开处理。遇到 503 时先等待并重试；管理员核查 :doc:`../manual/server/rest-api`。

固定 PDF 与解析版本
-------------------

.. code-block:: bash

   qatlas paper source-list qa_01m0qv0k4kajjb41fwaemspcrd --json
   qatlas paper parse-list qa_01m0qv0k4kajjb41fwaemspcrd --json
   qatlas paper pdf qa_01m0qv0k4kajjb41fwaemspcrd --source SOURCE_ID -o paper.pdf
   qatlas paper parse-json qa_01m0qv0k4kajjb41fwaemspcrd REVISION_ID -o parse.json

记录作品 ID、source/revision ID、SHA-256、来源和读取日期，便于后续重复实验。
``paper pdf`` 使用 originals API，与已退役的 ``paper get pdf`` 接口不同。

旧缓存与 originals API
----------------------

历史 ``paper_assets`` 中有 PDF、Markdown、JSON 路径，但还没有不可变 source 和
parse revision 记录时，``source-list`` / ``parse-list`` 可以返回空列表。
这不表示资产已被删除，普通 Markdown 读取仍可工作。

升级数据库只创建 originals 表及其约束，**不会自动把所有旧缓存回填成固定版本**。
读取或列出资产也不会触发迁移、覆盖或重新转换。需要 source/revision pin 时，
按 :doc:`cli` 的贡献流程登记实际 PDF 和 MinerU 产物，并核对服务器返回的
SHA-256；保留旧资产与来源证据。没有原始字节时不要用作品 ID 猜造 source ID。
