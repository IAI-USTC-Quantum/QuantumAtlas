QuantumAtlas 使用文档
=====================

**论文收集（Paper Collection） · 数据库（Database） · 多范式搜索（Search）**

QuantumAtlas 是一个聚焦论文资产的框架，只做三件事：

- **论文收集** —— 从 arXiv 等来源抓取论文 PDF，存入对象存储，并惰性收录新论文；
- **数据库** —— 以 PostgreSQL 注册表维护论文的唯一编号、身份与资产记录；
- **搜索** —— 多范式、可插拔的检索：输入一个 Search Entry，输出候选论文 ID 列表。

收录的论文会经由 MinerU（VLM 模型）批量转换为带图片的 Markdown，供后续阅读与检索使用。

本文档分两部分：**用户指南** 面向使用者，从 Web 界面、命令行（CLI）和 HTTP API
三个角度讲解如何使用；**开发指南** 面向贡献者，介绍代码组织与插件化架构路线。

.. note::

   本文档站点（``/doc``）是公开页面，无需登录。Web 界面与 API 需要 GitHub
   白名单账号登录，详见 :doc:`guide/api` 的认证章节。

.. toctree::
   :maxdepth: 1
   :caption: 用户指南

   guide/concepts
   guide/web
   guide/cli
   guide/api
   guide/search
   guide/mineru
   guide/admin

.. toctree::
   :maxdepth: 1
   :caption: 开发指南

   dev/layout
   dev/plugins
   dev/apps
   dev/versioning
