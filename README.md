# NewAPI channel recovery sidecar

Go sidecar，不调用 NewAPI 的“测试所有通道”功能，只处理 `status=3` 且 `auto_ban=1` 的渠道。测试成功后再次确认状态，再改为 `status=1`；失败不修改配置、不删除渠道。

## 部署

1. 在 NewAPI 后台创建专用管理员账号并生成 Access Token；将 Token 和该账号用户 ID 写入服务器上的 `newapi-sidecar/.env`，不要提交或发送 Token。
2. 确保 sidecar 加入 NewAPI 所在的 Docker 网络。若现有网络不是 `new-api-network`，修改 `docker-compose.yml` 最后一段的网络名。
3. 启动：`docker compose up -d --build`。

- `CHECK_INTERVAL_SECONDS`：检查间隔，默认 `60`
- `CHECK_CONCURRENCY`：并发数，默认 `1`
- `TEST_TIMEOUT_SECONDS`：请求超时，默认 `60`
- `SUCCESS_COUNT`：连续成功次数，默认 `1`
- `RECOVERY_GROUPS`：逗号分隔的分组；留空表示所有分组

默认每 1 分钟检查一次、并发数为 1。需要抗抖动时把 `SUCCESS_COUNT=2`。

NewAPI 后台请关闭“定时测试所有通道”；`SYNC_FREQUENCY=60` 会让成功后的状态最迟约 60 秒同步到内存缓存。
