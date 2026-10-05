QuantumAtlas 使用文档
=====================

.. rst-class:: docs-intro

   收集论文，确认身份，搜索证据，固定可复现的阅读版本。

QuantumAtlas 把论文资产存入对象存储，以 PostgreSQL 注册表维护统一作品编号，
通过多源检索找到论文。MinerU 将 PDF 转为带图片的 Markdown；不可变 source 和
parse revision 支持固定字节与解析版本。

.. grid:: 1 1 3 3
   :gutter: 3

   .. grid-item-card:: 从搜索到阅读
      :link: guide/reading
      :link-type: doc

      安装 CLI、确认 ``qa_`` 身份、读 Markdown，或固定 PDF 与解析版本。

   .. grid-item-card:: 使用 Web 界面
      :link: guide/web
      :link-type: doc

      搜索、选择下载，浏览论文与讨论；无需登录服务器。

   .. grid-item-card:: 调用 HTTP API
      :link: guide/api
      :link-type: doc

      认证、配额、搜索与资产接口；处理缓存未命中和重试。

.. note::

   文档可公开阅读。实际 Web 应用与 API 位于
   `线上实例 <https://qatlas.hfnl.app.chenzhaoyun.com/>`_，需要获授权的账号。
   GitHub Pages 是静态文档入口，不提供登录、Swagger 或业务 API。

先读 :doc:`guide/reading` 完成一次完整的论文获取，再按角色选用下面的指南。
管理员的开发文档由实例的管理入口提供；公开 Pages 只发布这套使用文档。

.. toctree::
   :maxdepth: 1
   :caption: 开始使用

   guide/reading
   guide/concepts
   guide/web
   guide/cli
   guide/api

.. toctree::
   :maxdepth: 1
   :caption: 检索、收录与管理

   guide/search
   guide/external-sources
   guide/mineru
   guide/admin

.. toctree::
   :maxdepth: 2
   :caption: 组件文档

   _collections/qatlas-cli/index
   _collections/qatlas-search/index
   _collections/qatlas-rag/index

.. toctree::
   :maxdepth: 2
   :caption: 平台参考与架构决策

   manual/contents

组件正文在对应仓库维护，构建按 ``components.lock.json`` 固定提交并生成统一
搜索索引。文档提交不等于已验证的服务版本组合；运行版本以实例健康接口为准。
平台参考含部分历史设计，当前操作请优先按使用指南执行。
