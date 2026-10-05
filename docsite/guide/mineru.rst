MinerU 转换
===========

正文读取使用所选 **冻结 PDF source** 的懒解析，产物为完整不可变 bundle：
真实 Middle + Markdown 及生产者提供的全部其他文件，不再只保留正文和图片。
已存 PDF 下载不要求 MinerU token，不触发推理；状态查询也不创建解析任务。

服务端 hosted V1 协议
---------------------

默认 hosted API 为 ``https://mineru.net/api``，走 V1 ``/api/v1``：
uploads/create → PUT 文件 → complete 的 file.id → parse/jobs，要求
``output_formats: [zip]``。``tier`` 默认 ``standard``，可选
flash/basic/standard/advanced；``ocr_mode`` 为 auto/ocr。

legacy V4 的 ``model_version``、language、formula/table 字段属于独立协议，
不能把 model 名称当成 V1 tier，或将旧配置静默套给新 API。
API token 与预算只由部署方配置，不进入用户可见响应或文档示例。

触发与 readiness
----------------

``GET /api/papers/{id}/read`` 的未固定修订、完整包未就绪请求可返回
202 + Operation-Location + Retry-After。同一 source 合并并发工作；状态轮询
不触发重复任务。来源/成员/manifest 全部核验并完成 PG 发布之后才 ready，
不能只凭进程 done、旧 Markdown 或历史 Middle 文件判断成功。

调度器/管理员补解析保留各自预算与状态界面，实际启用模式见 :doc:`admin`；
它们也必须使用确切 source，并按相同完整包发布规则生成新 revision。
不会为本功能批量迁移或重新解析全部历史论文。

完整上传与原件
--------------

``upload-mineru`` 必須有支持的 ``docvortex.middle`` / schema_version 2.0
Middle（``middle_json.json`` 或受支持 ``layout.json``）及
``markdown.md`` / ``full.md``。MD-only、ContentList-only、缺件或无效 schema
整包拒收；文字论文可不含图片。

全部合法 ZIP 原相对路径、文件名、字节保留，包括未知文件；没有改名、JSON
重序列化或按“已知后缀”静默丢文件。路径穿越、绝对/反斜杠路径、重复、symlink、
CRC错误拒收。重上传是新不可变 revision，overwrite 不改变历史解析/评论。

阅读视图、原始 Middle、完整包成员和定位语义见 :doc:`reading` 与
:doc:`../manual/server/paper-content`，上传限制见 :doc:`../manual/server/upload-api`。
