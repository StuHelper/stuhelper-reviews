---
type: spec
status: Accepted
updated: 2026-11-09
---

# 热门活动来源

## 协议与数据归属

活动采用 search 9c56ad3 的 discovery-activity.schema.json，版本 1.0.0。contracts 保存原始 Schema、来源提交和 SHA-256。测试核对固定摘要，再逐条验证 outbox 事件，拒绝未拥有的指标字段。

reviews 只发送 reviews 与 interactions。事件类型为 discovery.activity.v1，source 固定为 reviews，target.source 固定为 catalog。活动 ID 为 kind/base64url(规范对象 ID)/UTC 日期，base64url 不带填充，subject 等于活动 ID。

每个对象日维护独立单调修订。事件完整替换日快照，零值显式发送；零计数墓碑不会被迟到事件重新计数。事件、计数和修订历史在同一业务事务提交，并调用 pg_notify('reviews_outbox', '')。

日快照只含目标、日期、修订、可见性和本模块指标，不含账号、正文、身份原值或支付信息。每项公开计数为 0 至 1000000000，越界会返回错误并回滚本次计数写入。

## 评价与互动计数

reviews 统计当日首次发布、且当前发布可见的课程评价主帖。撤回后重新发布沿用首次发布日期，不在重新发布当天再加一。待审、隐藏、删除和到期对象不贡献计数。

interactions 统计当前有效的投票与已发布回复。每条有效投票计一，赞成改为反对不重复计数；取消修订原投票日，再次投票使用新的投票日。回复使用首次发布日期，父帖隐藏时所有子回复和投票一同退出计数。

每个讨论保存按对象日分组的有效贡献，再把变化量写入共享日快照。不同讨论的并发更新不会覆盖彼此。撤回、隐藏、恢复、永久删除和到期清理均修订原贡献日期，而非把旧互动移到操作当天。

课程评价明确向课程和其可信教师归属分别贡献指标，search 不自动复制。导师评论主帖不计课程评价数，但其有效投票和回复可贡献对应教师的 interactions。规范课程与教师 ID 来自 Catalog.Target 的权威结果。

需要公开身份证明的教师回复会在 CurrentActivity 中重新核验。明确撤销公开许可时，该活动桶暂不参与热门；事实服务故障返回错误。核验后重新读取日快照，避免并发撤回期间返回旧修订。

单次讨论维护最多 10000 个对象日贡献键；当前活动最多核验 100 条需要公开证明的回复，超限返回 unavailable。投票注销匿名化保留原投票日和有效计数，取消或目标状态变化仍由业务事实决定。

backend 的目录适配器必须优先使用 catalog.Object.CanonicalID，非别名时使用 ID。reviews.Catalog.Target.ID 与 favorites.ResolvedTarget.ID 均表示规范 ID，不能直接把旧别名作为统计目标返回。

## 公开接口与一致性

postgres.Repository.ActivitySnapshot(ctx, fromDay, toDay, cursor, limit) 返回 []Activity、next、error。日期必须为 UTC 零点，窗口左闭右开，limit 为 1 至 100。首轮 cursor 为空，next 为空表示结束。

分页绑定窗口、上限和 PostgreSQL 可见性快照，读取不可变历史中的首轮最新修订，包含零计数和不可见墓碑。分页期间的新增、修改及延迟提交不能进入旧快照。游标有效五分钟，窗口或上限变化会被拒绝。

Service.CurrentActivity(ctx, principal, activityID) 返回 Activity、bool、error。请求人由可信认证提供，每次检查当前目标权限；明确不可读或不存在返回 false，数据库和来源依赖故障返回错误，不表示来源未启用。

零计数快照仍可返回 true，Visible=false 表示该快照不贡献热度。当前查询不读取其他模块表；catalog 通过自己的公开读规则判断，其他来源通过所属模块公开接口取得当前目标事实。

统计目标按写入时的规范 ID 绑定。目录合并不搬运旧目标的历史计数，当前查询拒绝已经变成别名的旧目标。后续新增使用新的规范 ID，避免把不可证明的历史贡献复制到另一个对象。

OutboxChannel 为 reviews_outbox。PendingOutbox(ctx, limit, excludeSubjects) 每批最多 100 条，最多排除 1000 个主题，排除在数据库 LIMIT 前完成。ConfirmOutbox(ctx, id) 在消费者持久提交后调用，重复确认保留首次 sent_at。

内容事件和活动事件共用同一 outbox，relay 按 type 分发。backend 负责 LISTEN、启动和重连补读、60 秒兜底、退避及待处理主题；模块不扫描业务表寻找待投递变化。

## 清理、权限与验证

PruneActivityHistory(ctx, beforeDay, limit) 每次最多删除 1000 条旧修订，保留每个对象日的最新修订和墓碑。清理在同一事务提高保留修订号，已有分页游标失效，调用方须中止重建。

运行身份需要 activity_buckets 的读写、activity_history 的读取和追加、activity_retention 的读取，以及 outbox 的读写确认权限。历史删除权限只授予负责清理的身份。当前日快照和最新墓碑不自动删除。

接口均为公开 Go 端口，OpenAPI 路径和生成客户端没有变化。GOWORK=off 的隔离 PostgreSQL 检查覆盖修订单调、事务回滚无通知、Schema、固定快照、墓碑、并发和有界清理。backend 装配及实际热门页面组合另行验证。

| 场景 | 验证条件 |
| --- | --- |
| REV-ACTIVITY-01 | 本来源计数、权限、并发、回滚、事件 Schema、固定分页、墓碑和清理上限 |
