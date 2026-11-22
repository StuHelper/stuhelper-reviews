---
type: spec
status: Accepted
updated: 2026-11-21
---

# reviews 按账号信息查询

`accountquery` 提供 reviews 拥有的账号查询 Go 接口。backend 将已验证的账号、`admin.Scope` 和 information-query 请求转换为模块查询。

```go
func (s accountquery.Service) Query(
    ctx context.Context, principal accountquery.Principal,
    q accountquery.Query,
) ([]accountquery.Record, error)
```

`Kind` 必须为 `account`，`Value` 由 `Principal.Key()` 生成，记录 issuer 与 subject。记录按学校授权，群范围为 `*`，模块为 `reviews`。`Purpose` 与 `RequestID` 必填。

Limit 为 1–100，After 是上一页最后一条记录 ID。数据库先按当前授权和请求范围过滤，再按 ID 的 C 排序取排他分页。游标不会固定历史快照，后续页重新读取当前权限和记录。

`Fields` 沿用信息查询通用类别。reviews 提供评价类型、状态、对象 ID、标题和正文等业务字段，以及匿名作者关联。空列表返回业务字段。显式列表包含 `business` 时返回业务字段，包含 `identity` 时申请读取匿名作者关联。

能力为 `reviews.account-query` 与 `reviews.anonymous-author-read`，已登记 module.yaml。Authorizer.Scopes 必须从当前可信身份和授权读取本能力的范围，不接受客户端角色，也不使用其他域或管理写入能力替代。

授权范围与请求范围求交，空交集返回 ErrForbidden。授权、Repository 或敏感查看审计不可用返回 ErrUnavailable，且不返回部分结果。调用有五秒上限并传递取消信号。

Query 的 String 和 GoString 隐去所有查询参数。错误不包含查询值或数据库原始错误。宿主不能将 Query 序列化后写入日志。敏感审计只保存内部记录 ID、字段类别、用途与授权依据。

返回目标作者的课程评价与导师评论，包括 published、pending、hidden。草稿、撤回、删除和已到期内容不进入结果。署名以讨论身份为准，注销作者按匿名处理。

匿名记录只有在 Fields 包含 identity 且操作者具有对应学校的 anonymous-author-read 能力时参与分页。展开关联必须先写审计。正文不附带选课证明或认证标签。

宿主通过 `accountquery.Service{Repo: repository, Auth: authorizer, Audit: auditor}` 装配。`postgres.Repository.ReadAccount` 按已决范围过滤。查询入口通过 `Service.Query` 处理授权和敏感审计。

| 场景 | 验证条件 |
| --- | --- |
| IQ-02 | 本域授权、学校和群范围先过滤、稳定排他分页、运行身份隔离、敏感能力独立、审计失败和依赖不可用 |

`module.yaml` 登记 `IQ-02` 的 Go 测试。数据库测试使用只读身份，覆盖重复迁移、既有记录、范围、分页和写入拒绝。单元测试覆盖授权、字段、依赖失败和查询日志脱敏。
