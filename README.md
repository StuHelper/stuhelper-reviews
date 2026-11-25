---
type: index
status: Accepted
updated: 2026-11-26
---

# StuHelper Reviews

reviews 管理课程评价、导师评论、讨论身份、回复、追评、投票、草稿及审核历史。模块提供 Go Service、PostgreSQL Repository、HTTP Handler 和生成 SDK。平台规则见[课程与评价规格](https://github.com/StuHelper/stuhelper/blob/develop/docs/spec/academic-content.md)。

## 接口、依赖与数据归属

| 对象 | 归属与协作方式 |
| --- | --- |
| 课程、学期、规范 ID 和选课、任课记录 | 通过 catalog 公开接口读取。评价只保留与发布有关的归属与证据修订 |
| 当前准入、认证披露和平台昵称 | 宿主注入 `Access`、`DisclosureProvider` 与 `Profiles`。公开读取重新校验资格与授权 |
| 审核策略和任务 | moderation 检查发布策略、接收提交后的事件并下达决定。reviews 核对权限和对象修订，保存正文及实际状态 |
| 收藏 | favorites 管理收藏记录。reviews 通过其公开接口核对本人收藏及名称查询权限 |

`module.yaml` 声明必需依赖 platform、catalog、moderation，可选依赖 favorites、profiles、verification、identity。`reviews.read`、`reviews.write`、`reviews.moderate` 以 identity 为能力依赖。账号查询和匿名作者反查各有独立授权，详见[账号查询](docs/spec/account-query.md)。

HTTP 路径位于 `/api/v1/reviews`。公共详情、列表、编辑器上下文、评分展示配置和统计检查当前学校准入。本人评价、草稿与投票回执检查登录和归属。准入失效后仍可读取本人记录并取消已有投票。投票回执仅包含条目、讨论、学校、课程或导师对象等标识及本人投票值。

浏览器写请求校验 Origin 的协议和主机。带 JSON 正文的请求要求 `application/json`，可带字符集。反向代理终止 TLS 时，宿主通过 `Handler.PublicOrigin` 提供可信的外部来源。

## 发布与个人记录

课程评价绑定学期，学校数据认证有效且有匹配记录时，教师选项来自本人该课程该学期的选课。其余情况使用本学期公开任课关系。未指定教师时保存全部候选教师的归属。管理员可按权威来源更正归属，保留修订和审计。课程评价发布时保存选课状态，学校数据不可用时为 unknown。导师评论提供文字内容。

首次发布时间决定默认 5 分钟的删除与撤回窗口。撤回隐藏讨论并生成 24 小时草稿。重新提交按当前规则处理，保留首次时间、回复和投票。过期记录立即停止读取，清理任务按讨论移除。管理逻辑删除清除撤回期限并保留审核历史。

普通草稿含回复草稿，默认每人最多 200 条。发布与移除草稿在同一事务完成，同键重试返回原结果。关联校验失败时仍可保存文字，`reference_state` 表示校验状态，`reference_ids` 只列出已验证关联。本人名称查询每次最多 100 个 ID，任一 ID 缺少本人记录依据时整批返回 404。

学生可逐项选择公开姓名、性别及学院，教师可逐项选择姓名、性别、教师标识及单位。教师回复本人曾任教课程的任一学期评价时，必须公开获授权的真实姓名和教师标识。必需授权失效后，回复停止对外展示。作者保留本人读取权限。

账号注销通过可信端口持久登记。删除私人草稿与幂等回执，公开评价按匿名显示，投票逐条使用随机标识保留并继续计入统计。注销标记拦截并发或后续用户写入及管理署名切换。

## 阅读、统计和管理

评分计算、列表选项、追评和个人待办见[评分摘要与评价视图](docs/spec/ui-data.md)。教师评分先算各规范课程均值，再对有样本的课程等权平均。公开统计提供等级、样本量和课程数。学校显示文案继承全局配置，内置默认覆盖八种语言。

管理端支持学校范围内的原文及历史读取、`review_requested` 事后复核队列、优质认定与教师归属更正。每次读取详情或历史单独留审计。批量通过每次最多 20 项。本人可显式查询脱敏审核原因。

`postgres.Repository.ReadAudit` 供可信宿主传入当前授权范围，按时间、模块和 ID 降序分页。一次最多 101 条，时间范围左闭右开。PostgreSQL 快照排除回填及延迟提交，清理或修改审计后须重新查询。普通作者与投票人的操作者字段统一为 `anonymous`。

搜索投影和可靠投递见[搜索来源](docs/spec/discovery.md)。热门活动计数和日快照见[活动来源](docs/spec/activity.md)。

## 本地检查

```sh
make setup
bash scripts/check-with-postgres.sh
```

以 `contracts/reviews.openapi.json` 为 HTTP 契约来源，执行 `make generate` 更新 SDK。迁移身份执行 `postgres.Schema`，运行身份限定在 reviews schema。`make check` 检查生成一致性、模块边界、文档、Go 构建与竞态测试，以及 Actions 配置。

`module.yaml` 的 `scenario_tests` 使用 `文件#测试函数` 指向实际测试。CI 在隔离 PostgreSQL 上验证资格与学校范围、归属、并发写入、撤回和到期、草稿与审核、账号注销及审计。隔离数据库不使用生产数据。

## 生产基线与待验证事项

StuHelper v0.1.31 固定 Reviews v0.1.8。HTTP OpenAPI 版本为 0.9.0，发行 tag 标识源码组合。生产验收范围见 [StuHelper v0.1.31](https://github.com/StuHelper/stuhelper/releases/tag/v0.1.31)。

真实账号的评价提交、编辑和删除由负责人按 Ops 生产基线验收。业务规模下的性能基准待验证。
