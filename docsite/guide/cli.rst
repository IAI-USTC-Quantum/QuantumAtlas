命令行（qatlas CLI）
======================

安装
----

.. code-block:: bash

   # 从 PyPI 安装为全局工具（推荐）
   uv tool install quantum-atlas
   # 或以可编辑模式安装本地检出（贡献者）
   uv tool install . --editable --force

   qatlas --help

命令总览
--------

.. list-table::
   :header-rows: 1
   :widths: 18 82

   * - 命令
     - 说明
   * - ``qatlas config``
     - 管理客户端配置文件（``~/.config/qatlas/config.yaml``，YAML 唯一配置源）
   * - ``qatlas auth``
     - 管理各服务器的 PAT / 会话令牌（``login`` 走 OAuth Device Flow）
   * - ``qatlas paper``
     - 从服务器取论文资产：``get markdown`` / ``get pdf`` / ``status`` /
       ``mineru-lease``；缓存未命中时自动触发服务端抓取与转换（LRO 轮询）
   * - ``qatlas contrib``
     - 贡献者工作流：``contrib pdf`` 上传 PDF；``contrib mineru`` 用自己的
       MinerU token 本地转换并回传（队列 / 单篇 / watch 守护模式）
   * - ``qatlas parser``
     - 本地工作区命令：抓取并解析 arXiv 论文（不经过服务器）

别名：``papers`` → ``paper``，``parse`` → ``parser``。

示例
----

.. code-block:: bash

   # 拉取论文 Markdown / PDF（未收录时服务端惰性抓取）
   qatlas paper get markdown quant-ph/9508027 -o paper.md
   qatlas paper get pdf 10.1103/PhysRevLett.103.150502 -o paper.pdf

   # 上传本地 PDF（需 papers:write 权限的 PAT）
   qatlas contrib pdf quant-ph/9508027v1 --pdf paper.pdf

   # 本地 MinerU 转换并回传
   qatlas contrib mineru 2501.00010v1
   qatlas contrib mineru --watch

论文 ID 支持多种形式（服务端自动补全）：带版本 arXiv ID（``0811.3171v3``）、
裸 arXiv ID（补最新版本）、裸旧式编号（补 ``quant-ph/`` 分类）、DOI。

配置与认证
----------

- 客户端配置只有 YAML 一个来源：``~/.config/qatlas/config.yaml``
  （``qatlas config set <key> <value>``，敏感值走 stdin 隐藏输入）；
- 认证用 PAT：``qatlas auth login`` 发起设备流，浏览器里批准后令牌自动落盘；
- 论文读取需要 ``papers:read``，上传 / 删除需要 ``papers:write``，见 :doc:`api`。

.. note::

   ``qatlas search`` 与 ``qatlas rag`` 都由独立插件提供（entry-point
   发现，安装对应的插件包后命令自动出现在 CLI 中，未安装时会提示
   安装方法）：前者由 qatlas-search 仓库提供，后者由 qatlas-rag 仓库
   提供，详见 :doc:`插件化架构与路线图 </dev/plugins>`。

服务器端运维命令（``qatlasd``）
-------------------------------

Robust Downloader 的验收 / 压测工具随 qatlasd 二进制发布：

.. code-block:: bash

   # 单篇论文跑完整下载阶梯，打印策略轨迹
   qatlasd downloader probe 10.1038/s41586-024-07806-9 arXiv:2401.12345

   # 从 OpenAlex 随机抽 N 篇论文压测（可选 ``--search`` 过滤领域）
   qatlasd downloader probe --random 30 --search "quantum computing"

   # 指定远程 downloaderproxy（校园出口）与 agent 兜底
   qatlasd downloader probe --random 10 --proxy http://ag-workstation:8602

   # 机器可读 JSON 输出；任一失败退出码为 1（适合自动化）
   qatlasd downloader probe --random 5 --json

需要 ``paper_access.enabled: true``（读取 ``~/.qatlas/config.yaml``）。
详见 :doc:`search` 的 :doc:`search <search>` 一节。
