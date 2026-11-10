---
type: spec
status: Accepted
updated: 2026-11-09
---

# 搜索来源与可靠投递

## 搜索来源与事件投递

contracts/discovery-event.schema.json 固定 search 9e791c6 的 discovery.v1 约定，来源提交和校验值记录在相邻 source.json。投影只有公开标题、学校、课程与教师归属、修订、可见状态和时间，不包含正文、作者、认证材料或评分。

搜索文档只覆盖课程评价主帖，kind 为 review。教师归属来自评价的可信归属快照，不复制选课证据。导师评论与讨论回复暂不建立搜索文档。发布、修订、撤回、审核隐藏、恢复、逻辑删除、永久删除和到期清理均更新对应对象投影。

每次来源对象保存都在业务事务内追加 discovery_history，并写入 reviews.discovery.v1 outbox 与 pg_notify('reviews_outbox', '')。不可见文档是墓碑，标题为空且不携带课程和教师关联。原始对象删除后，墓碑仍保留。

事件采用 CloudEvents 1.0。source 为 reviews，subject 等于 data.target.id，事件 ID 由对象 ID 与修订确定。相同请求重放沿用业务幂等回执，不重复写入投影事件。普通业务事件也使用该封套，只携带业务修订。

模块导出 OutboxChannel。postgres.Repository.PendingOutbox(ctx, limit, excludeSubjects) 返回待发送 Envelope，每批 1 至 100 条，最多排除 1000 个主题。nil 与空集合等价，排除在 LIMIT 前执行，同主题按写入顺序读取。

outbox 只用 sent_at 表示确认状态，未发送部分索引支持空闲与积压查询。读取不认领、不确认事件。ConfirmOutbox(ctx, id) 在所有订阅消费者持久提交后调用，重复确认保留首次时间，不存在的 ID 返回 not_found。

backend relay 负责 LISTEN、启动和重连补读，以及每 60 秒的未发送索引兜底。失败按 5 秒起的指数退避重试，最长一小时，累计 24 小时转待处理。excludeSubjects 接收退避和待处理主题，消费者按事件 ID 去重。

Service.DiscoveryCurrent(ctx, principal, ids) 接收 1 至 100 个 ID，每次检查当前内容准入和学校，只返回当前发布且未过期的课程评价文档。撤销准入返回空集合，依赖故障返回错误。

Service.DiscoveryMyVotes(ctx, principal, cursor, limit) 返回 Items 与 NextCursor，choice 为 up 或 down，每个目标重新检查当前阅读权限。分页游标按已检查的位置前进，隐藏结果不阻断后续页。已有 MyVotes 继续提供本人操作回执。

旧 Handoff 和事务内 Events、CompleteEvent、FailEvent 已移除。relay 对普通评价事件按当前 Inspect 的修订和 ReviewRequested 判断是否交给审核，不能用一个消费者的确认跳过其他订阅者。

postgres.Repository.DiscoverySnapshot(ctx, cursor, limit) 供受信重建任务调用，返回 Items 和 Next，单页 1 至 100 条。空 cursor 开始，空 Next 结束。不可变历史与 PostgreSQL 可见性快照固定首轮成员，分页按 ID 字节序递增。

分页期间的修改、删除和延迟提交不会替换首轮版本。游标五分钟有效，历史修改或清理会使旧游标失效，必须中止重建。历史与墓碑不自动清理，保留策略须结合消费者重放期限单独确认。

本次只新增公开 Go 来源接口，不新增或修改 OpenAPI 路径。运行身份需要读取 discovery_history、discovery_retention，追加 discovery_history，并读取、写入和确认 outbox。历史的修改与删除保留给迁移或维护身份。

| 场景 | 验证条件 |
| --- | --- |
| REV-DISCOVERY-01 | 状态变化、墓碑、Schema 校验、权限、回滚与固定快照 |
| REV-DISCOVERY-02 | 当前有效投票、资格撤销、取消、分页和本人回执区别 |
| REV-OUTBOX-01 | 提交通知、回滚无通知、主题排除、积压与幂等确认 |
