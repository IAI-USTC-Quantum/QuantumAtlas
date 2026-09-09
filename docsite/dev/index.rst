QuantumAtlas 开发文档
=====================

面向贡献者与运维的开发文档（**仅管理员可见**，与公开的使用文档分离托管）。
文档内容覆盖代码组织、插件化架构、外围 app 接入、版本兼容协议与发布部署流程。
下载器章节包含主动接入的多 worker 调度、归档确认和重启恢复；部署章节提供
主服务启用、节点注册审批及旧 proxy 迁移步骤。文档描述实现和操作约定，不表示
某个线上实例已完成升级。

.. toctree::
   :maxdepth: 1

   layout
   plugins
   apps
   downloader
   versioning
   release
   prod-deploy
