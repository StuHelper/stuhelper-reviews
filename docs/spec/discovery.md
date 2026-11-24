---
type: spec
status: Accepted
updated: 2026-11-24
---

# 搜索来源与可靠投递

## 投影、事件与读取

`contracts/discovery-event.schema.json` 固定 search 的 `discovery.v1` 约定，来源提交与校验值见相邻来源记录。投影只含公开标题、学校、课程与教师归属、修订、可见状态和时间。

搜索文档只覆盖课程评价主帖，kind 为 review。教师归属来自评价的可信归属快照。发布、修订、撤回、审核隐藏、恢复、逻辑删除、永久删除和到期清理均更新对应对象投影。

每次来源对象保存都在业务事务内追加 discovery_history，并写入 reviews.discovery.v1 outbox 与 pg_notify('reviews_outbox', '')。不可见文档是墓碑，标题为空且不携带课程和教师关联。原始对象删除后，墓碑仍保留。

事件采用 CloudEvents 1.0。source 为 reviews，subject 等于 data.target.id，事件 ID 由对象 ID 与修订确定。相同请求重放沿用业务幂等回执，不重复写入投影事件。普通业务事件也使用该封套，只携带业务修订。

模块导出 OutboxChannel。postgres.Repository.PendingOutbox(ctx, limit, excludeSubjects) 返回待发送 Envelope，每批 1 至 100 条，最多排除 1000 个主题。nil 与空集合等价，排除在 LIMIT 前执行，同主题按写入顺序读取。

outbox 只用 sent_at 表示确认状态，未发送部分索引支持空闲与积压查询。读取不认领、不确认事件。ConfirmOutbox(ctx, id) 在所有订阅消费者持久提交后调用，重复确认保留首次时间，不存在的 ID 返回 not_found。PruneOutbox 与 PruneReplays 分别按确认时间和到期时间提供每次最多 1000 条的有界清理。

运行身份负责读取和确认 outbox。已确认事件由具备 outbox 表 SELECT、DELETE 权限的独立维护身份清理。

postgres.Repository.OutboxMetrics(ctx) 与 Service.OutboxMetrics(ctx) 返回未确认积压量 PendingCount 与最老未发送时间 OldestPendingAt。

backend relay 负责 LISTEN、启动和重连补读，以及每 60 秒的未发送索引兜底。失败按 5 秒起的指数退避重试，最长一小时，累计 24 小时转待处理。excludeSubjects 接收退避和待处理主题，消费者按事件 ID 去重。

Service.DiscoveryCurrent(ctx, principal, ids) 接收 1 至 100 个 ID，每次检查当前内容准入和学校，只返回当前发布且未过期的课程评价文档。撤销准入返回空集合，依赖故障返回错误。

Service.DiscoveryMyVotes(ctx, principal, cursor, limit) 返回 Items 与 NextCursor，choice 为 up 或 down，每个目标重新检查当前阅读权限。分页游标按已返回的主帖 ID 前进，隐藏结果不阻断后续页。已有 MyVotes 继续提供本人操作回执。

relay 通过 `Inspect` 核对普通评价事件的当前修订，再按 `ReviewRequested` 和待审追评标识向 moderation 建立任务。所有订阅消费者完成持久化后才确认事件。

postgres.Repository.DiscoverySnapshot(ctx, cursor, limit) 供受信重建任务调用，返回 Items 和 Next，单页 1 至 100 条。空 cursor 开始，空 Next 结束。不可变历史与 PostgreSQL 可见性快照固定首轮成员，分页按 ID 字节序递增。

分页期间的修改、删除和延迟提交不会替换首轮版本。游标五分钟有效，历史修改或清理会使旧游标失效，必须中止重建。

搜索旧修订自被替代起、最小墓碑自产生起默认保留 90 天。清理墓碑前须确认各订阅方已处理该墓碑、较早事件无法重放且无有效 `legal_hold`。墓碑仅保存识别对象及修订顺序所需的数据。

全文、投影、历史修订和事件载荷中仍可关联个人的信息，按适用期限中较早到期的时间删除或匿名化。到期后只保留必要且无法重新识别个人的元数据。搜索记录的自动清理待实施。[现行条款第 4 节](https://stuhelper.com/terms.html) · [数据保留与删除](https://github.com/StuHelper/stuhelper/blob/develop/docs/spec/data-retention.md)

这些来源接口供宿主通过 Go 调用。运行身份读取 `discovery_history`、`discovery_retention`，追加 `discovery_history`，并读写和确认 outbox。历史修改及删除权限限于迁移或维护身份。

| 场景 | 验证条件 |
| --- | --- |
| REV-DISCOVERY-01 | 状态变化、墓碑、Schema 校验、权限、回滚与固定快照 |
| REV-DISCOVERY-02 | 当前有效投票、资格撤销、取消、分页和本人回执区别 |
| REV-OUTBOX-01 | 提交通知、回滚无通知、主题排除、积压与幂等确认 |
| RET-12 | 旧修订被替代满 90 日后清理，墓碑须同时有投递、无旧重放和无有效保留的证据。 |

`module.yaml` 登记来源、有效投票及 outbox 的隔离测试。本人投票回执可包含导师评论与回复。搜索投票的对象仅限符合当前学校范围且已发布、未到期的课程评价主帖。
