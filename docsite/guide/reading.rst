从搜索到可复现阅读
==================

先找到论文，再确认身份，最后选择派生阅读视图或固定原始资产。
本页对应新的不可变内容 API；客户端须与服务端的 ``major.minor`` 对齐，
RC 使用部署方确认的完整版本，不能假定旧客户端已支持新接口。
安装、登录及既有配置保留见 :doc:`cli`。

身份与版本不是同一件事
----------------------

- ``qa_…`` 是作品身份，不固定 PDF 字节或解析。
- ``source_id`` 固定 PDF 的确切字节与 SHA-256；同一作品相同 SHA 可复用 source。
- arXiv ``vN`` 是上游语义版本，来源标签可能为 ``arxiv:vN`` 或
  ``arxiv:IDvN``；相同字节的两个语义版本可以共享 source。
- ``revision_id`` 固定一次完整解析发布；重解析/重上传产生新 revision，
  current 只是可变指针，旧评论不迁移。
- ``S3VersionId`` 是对象存储物理版本，不是上述任何公开 pin，不能用它替代
  source、arXiv 版本或解析修订。存储版本恢复也不得覆盖固定身份的字节。

查找与确认身份
--------------

``qatlas search`` / ``qatlas match`` 需要各自插件，也可从 Web 搜索或 :doc:`api`
取得作品 ID。match 只检查已有 registry；搜索渠道错误不能解释为论文不存在。

.. code-block:: bash

   qatlas search 'quantum linear systems' --json
   qatlas match 1010.4458 --json
   qatlas paper get metadata qa_…

可定位、可接续的 JSON 阅读
------------------------------------------------------------

.. code-block:: bash

   qatlas paper read qa_… --limit 30000 --json
   qatlas paper read qa_… --source-id SOURCE_ID --revision REVISION_ID --page 5 --block 12 --json
   qatlas paper read qa_… --cursor 'next_request.cursor 的原字符串' --json
   qatlas paper read qa_… --no-wait --json

stdout 是完整的 **Middle 派生阅读信封**，不是原始 Middle/ContentList JSON。
响应保留 source/revision/PDF与artifact摘要、renderer、content、request_scope、
content_ranges、truncated、next_request 和 warnings。``page`` 与 ``block`` 为
1-based 的原生产者编号，block 必须带 page，非连续 index 不重编号；预算为
Unicode 正文字符，默认 30000、上限 100000。

truncated=true 时将 ``next_request.cursor`` 原样传回；游标固定来源、修订、
摘要、renderer 和选择范围。cursor-only 会先解 pin 再加载，不会误选最新 current。
显式 pin/选择冲突为 409，格式错误为 400，缺件为 404，完整性失败为 422，
存储/registry 不可用为 503；不能用重新解析或回退其他版本掩盖固定原件故障。

未就绪的无 pin 正文请求可触发懒解析并返回 202、Operation-Location、Retry-After。
CLI 有界轮询 read/status，只有完整包与来源核验 ready 后重取原请求。
状态请求不下载、不创建解析任务；进程 Done 或历史 Middle 文件存在不代表 ready。
``--no-wait`` 仅返回异步状态和轮询位置，进度始终在 stderr。

固定 PDF 与原始解析包
---------------------

.. code-block:: bash

   qatlas paper pdf qa_… --source-id SOURCE_ID -o paper.pdf
   qatlas paper pdf 0811.3171v2 --version v2 --no-cache -o paper-v2.pdf
   qatlas paper source-list qa_… --json
   qatlas paper parse-list qa_… --json
   qatlas paper parse-json qa_… REVISION_ID -o middle-original.json

``paper pdf`` 直接访问鉴权 ``/api/papers/{id}/pdf``；不先枚举 source，所以旧 PDF
尚未登记 source 不会阻断获取。source/version 互斥，显式 pin 无匹配不换版本。
规范 ID 和别名均校验完整 SHA 后才输出；缓存命中也先确认当前服务端授权/开关，
禁用/401/403 不可用旧缓存伪装成功。已存 PDF 不需要 MinerU token 或推理。

完整包的 ``/parses/{revision}/manifest`` 返回各成员原始相对路径/大小/SHA；
``/parses/{revision}/files/{original_relative_path}`` 返回其原字节，含所有 JSON、
Markdown、图片和未知合法成员。生产者原名不被改写。``/json`` 和 ``paper parse-json``
仍只返回固定 revision 的原始 Middle artifact，不是阅读视图；历史评论依然固定
原 source/revision，不拿新的解析替换旧锚点。

开关、懒迁移与上传
------------------

所有内容/PDF/raw JSON/block 原件/图片/figures 均要求
``paper_access.enabled`` 和当前身份 ``papers:read``；关闭 404，无存储或解析副作用。
元数据列表和评论不能变成绕过开关的原件入口。

新内容在独立 ``qatlas-content`` 桶的 ``content/`` 命名空间中不可变发布。
数据库迁移不做 bulk backfill；首次内容/PDF访问仅按需冻结旧 PDF，不复制旧 MD、
JSON 或图片，不删除旧桶。正文需要时才对冻结 source 解析，不能把旧派生资产当 ready。

PDF 导入别名永久绑定首次成功的确切 PDF；同别名不同字节即使 ``overwrite=true``
也返回 409。完整解析上传须支持的 Middle + Markdown，MD-only/缺件拒收；所有
原成员名/字节保留，核验成员、manifest 和 PG 发布后才切 current。重上传是新
revision，overwrite 不修改旧 revision。

完整 API、清单和错误契约见 :doc:`../manual/server/paper-content`，上传入口见
:doc:`../manual/server/upload-api`；历史存储记录不代表当前覆盖写规则。
