论文编号体系
============

每篇论文在 QuantumAtlas 中有三层标识：

.. list-table::
   :header-rows: 1
   :widths: 20 30 50

   * - 标识
     - 形式
     - 说明
   * - ``paper_id``
     - ``qa_`` + 26 位 ULID
     - 全局唯一主键，收录时铸造（mint），终生不变
   * - 身份（identity）
     - arXiv ID / DOI / OpenAlex ID / 标题哈希
     - 用于去重与跨来源合并；同一篇论文的多个身份归并到同一个 ``paper_id``
   * - ``asset_id``
     - 自增整数
     - 论文资产编号，对应一份具体的 PDF 资产（某个 arXiv 版本或正式出版版本），
       指向对象存储中的相对路径

论文状态（``status``）：

``pending``\（已收录、资产抓取中）→ ``ready``\（PDF 已入库）。
MinerU 转换完成后，资产记录会带上 Markdown 路径与图片数。

惰性收录（Lazy Ingestion）
--------------------------

搜索命中一篇数据库里 **没有见过** 的论文时（例如一个陌生的 arXiv ID），
系统会自动为它铸造 ``paper_id``、写入注册表，并在后台抓取 PDF 入库——
下次再搜到它时就是本地命中了。

搜索响应中的 ``created: true`` 表示本次搜索触发了新收录。

去重与合并
----------

所有身份注册都经过唯一入口：同一篇论文无论从 arXiv ID、DOI 还是 OpenAlex ID
进入，都会归并到最早铸造的 ``paper_id`` 上，并发冲突时自动重查，不会产生重复记录。
