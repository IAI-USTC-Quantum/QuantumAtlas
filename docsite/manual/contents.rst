平台与运维参考
==============

本部分完整收录平台、客户端集成、服务端运维、数据格式与架构决策正文，
与使用手册及开发指南互补。正文统一位于 ``docsite/manual/``，
使用 Sphinx/MyST 构建，不再依赖 MkDocs / Material 的主题语法或构建 hooks。

架构决策与较早的运维记录按原文保留历史背景；迁移格式不代表旧接口、环境变量或
尚未实施的规划重新生效。执行操作前请核对对应版本的配置与 API 契约。

概览与入门
----------

.. toctree::
   :maxdepth: 1

   平台总览 <index>
   getting-started
   contributing

概念与架构
----------

.. toctree::
   :maxdepth: 1

   concepts/index
   concepts/architecture
   concepts/data-flow
   concepts/auth-model
   concepts/storage-architecture

Python 客户端
-------------

.. toctree::
   :maxdepth: 1

   client/index
   client/ingest-papers
   client/upload-assets
   client/parse-with-mineru
   client/contribute-content
   client/manage-credentials
   client/external-plugins
   client/cli-qatlas

Go 服务端
---------

.. toctree::
   :maxdepth: 1

   server/index
   server/install
   server/docker
   server/reverse-proxy
   server/server-config
   server/downloader
   server/downloader-fleet
   server/downloader-workers
   server/github-oauth
   server/rustfs
   server/rest-api
   server/upload-api
   server/pg-schema
   server/api-explorer
   server/cli-qatlasd
   server/health-and-monitoring
   server/backup-and-upgrade
   server/operations
   server/migration-storage-layout

参考与数据格式
--------------

.. toctree::
   :maxdepth: 1

   reference/index
   reference/glossary
   reference/env-vars
   reference/arxiv-ids

架构决策（ADR）
---------------

.. toctree::
   :maxdepth: 1

   adr/index
   adr/0001-plugin-type-taxonomy
   adr/0002-theorems-wiki-builtin-pull-plugins
   adr/0003-plugins-own-their-domain
   adr/0004-no-claim-collection-this-iteration
   adr/0005-claim-contrib-agent-webui
   adr/0006-postgres-central-store-not-mysql
   adr/0007-claim-references-and-lookup-boundary
   adr/0008-claim-authoring-moves-to-the-lean-repo
   adr/0009-papers-paper-assets-catalog-redesign
   adr/0010-openalex-works-inline-citations-and-audit
   adr/0011-by-id-asset-reads
   adr/0012-lazy-load-module-singleflight-durable-store
   adr/0013-corpus-schema-base-index-split

关于项目
--------

.. toctree::
   :maxdepth: 1

   about/index
   about/design-philosophy
   about/faq
   about/credits
   about/external-data-sources
   about/license-and-attribution
   about/terms-of-service
