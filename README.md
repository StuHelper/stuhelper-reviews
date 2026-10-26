# StuHelper Reviews

课程评价、导师评论、讨论、投票、收藏和草稿模块，提供 Go 服务、PostgreSQL Repository、HTTP Handler 与生成 SDK。课程目录、当前资格、内容审核和平台昵称通过公开接口注入。

课程评价绑定学期，教师仅取自请求人在该学期的可信选课记录。不指定教师时保存该学期任课教师并集。归属快照在重新发布时保持不变。导师评论只有文字，不参与课程评分。

教师评分先计算各课程均值，再对课程等权平均。接口只返回五级展示等级、样本量和课程数，分页查询分课程统计不改变总体结果。匿名公开结果不包含账号或选课证明。

首次发布后的编辑窗口默认 5 分钟。撤回隐藏整条讨论并保存 24 小时草稿，重新发布保留首次时间、回复和投票。过期读取立即拒绝，清理任务分批删除。普通草稿不使用这个期限。

## 使用与检查

```sh
make setup
bash scripts/check-with-postgres.sh
```

先修改 `contracts/reviews.openapi.json`，再执行 `make generate`。首次迁移由独立身份执行 `postgres.Schema`，运行身份仅访问 reviews schema。Handler 解析输入，Service 检查当前资格与对象状态，Repository 保存数据。

重复写入使用幂等键；同键不同内容拒绝。业务变化、审计、outbox 在同一事务提交。审核交接通过 Handoff 重试，审核决定按决定 ID 与内容修订拒绝重复效果和旧决定。

PostgreSQL 测试覆盖资格缺失、不同学期教师、匿名预览、身份继承、并发重复提交、统计、撤回、到期、草稿分页、投票和审核。测试使用合成身份与独立数据库。

真实身份和校园资格由应用提供。缺少事实或依赖时拒绝相应操作，不以演示身份代替。真实账号、存量迁移、备份恢复和压力基准仍待验收。

业务规则见 [课程与评价规格](https://github.com/StuHelper/stuhelper-next/blob/develop/docs/spec/academic-content.md)。
