---
type: guide
status: Accepted
updated: 2026-11-21
---

# 工程约定

遵循主工作区的 [AGENTS.md](https://github.com/StuHelper/stuhelper/blob/develop/AGENTS.md)。评价、讨论身份、草稿、投票与历史由 reviews 管理。目录、认证、收藏和审核通过公开接口访问，SQL 留在 `postgres/`。

公共内容检查当前学校准入与可见性，本人入口检查账号归属。以 `contracts/reviews.openapi.json` 维护 HTTP 接口，然后使用 `make generate` 更新 SDK。用 `module.yaml` 登记依赖及业务场景对应的测试函数。

使用 `make setup` 安装锁定依赖，使用 `bash scripts/check-with-postgres.sh` 执行隔离检查。运行身份和迁移身份分离，测试使用合成身份和教务记录。
