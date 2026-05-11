# WindsurfAPI-Go 交接备忘

> 写入时间：2026-05-10 21:xx（今天结束这一轮 session 之前）
> 上一份顶层计划：`PLAN.md`（17:02 那版，已过期，建议改名 `PLAN.archive.md`）
> 关联仓库：`../WindsurfAPI`（Node 主线，仍在跑生产）

---

## 一句话状态

**LangServer 端到端 send 还没打通**。`cmd/ls-smoke` 现在只能跑到「拿到 LS 入口、构造请求、被 protovalidate 拒掉 / transport 抖一下就退」，离「真的从 Cascade 拿到一段流式 token」还差最后一脚。

仓库结构（截至刚才 `ls -la`）：

```
WindsurfAPI-Go/
├── PLAN.md            # 17:02 那版，过期
├── Makefile
├── go.mod / go.sum
├── cmd/               # ls-smoke 在这里；server 入口还是空壳
├── configs/
├── data/              # data/windsurf.db sqlite，账号表已建但还没塞真号
├── internal/          # 16 个子目录，grpc/auth/db/... 都铺好了骨架
└── ls-smoke           # 9.7MB 已编译产物
```

---

## 真正卡点（按风险排序）

1. **字段命名漂移**：`account` 表里的 token 列到底叫 `firebase_token` 还是 `firebase_id_token`，Go 结构体那边有没有 tag 错配，**还没用真数据 round-trip 验证过**。Node 那边历史上踩过一次，不锁死会再踩。
2. **protovalidate 错误分流没有真流量验证**：`isProtovalidateErr` / `isTransportErr` 三类错误分支是按 Node 经验写的，但 Go 的 grpc 错误形态稍有不同，没跑过真请求所以不知道分得对不对。
3. **Cascade cold stall 75s**：Node `findings.md` 里记的，冷启动会 stall 75 秒，Go 这边 dial timeout / context deadline 还没按这个数对齐过，跑 send 之前最好先看一眼。
4. **reuse fingerprint 漂移 / cache_read 是稳定前缀不是累加**：这两条是 Node 主线最近才确认的协议事实，Go rewrite 的缓存层如果直接按「累加」实现就会废。

---

## 下一轮接手建议（按时间盒）

1. **20 分钟**：在 `data/windsurf.db` 用 sqlite3 cli 插一条真账号，写一个 `account_test.go::TestSelectAccountReturnsRawToken`，断言 `acc.FirebaseToken == "devin-session-token$..."` 原文回来——**先把字段命名风险锁死**。
2. **1 小时**：选项 A 落地。在 `cmd/ls-smoke` 加 `-mode=send`，目标输出 `"hi"` / `"hello"` 之类的真 token 流。沿用 `isProtovalidateErr` / `isTransportErr` / 其他 三类错误分流。
3. **30 分钟**：成功之后，把今天 + 这一轮的 raw response bytes 各存一份到 `internal/grpc/testdata/`，写 round-trip fixture 测试，把胜利上锁。
4. 之后再考虑 `cmd/server` 真启 / Dashboard / 高可用调度。

---

## 不要做的事

- 不要在没拿到一次真 token 流之前去写 Dashboard / 调度器 / HA。protocol 层没钉死之前往上盖楼一定返工。
- 不要直接信 `PLAN.md` 17:02 那版的细节排期，里面对「proto 已通」的假设是乐观的。
- 不要把 Node 的 `dynamic-proxy.js` 行为照抄——Node 那边有些是为了 PM2 多进程妥协的，Go 单进程不需要。

---

## 参考文件

- 原计划文档：`../WindsurfAPI/docs/windsurfapi-go-rewrite-plan.txt`（631 行，分阶段计划，仍是主权威）
- Node 主线最新进展：`../WindsurfAPI/progress.md`（截至 May 10 18:10）
- Node 协议踩坑记录：`../WindsurfAPI/findings.md`（关键经验：Cascade cold stall 75s、reuse fingerprint 漂移、cache_read 是稳定前缀不是累加）
- Node 协议核心文件：`../WindsurfAPI/src/{client,langserver,handlers/chat,models,auth,dynamic-proxy}.js`
- Node 当前任务计划：`../WindsurfAPI/task_plan.md`

---

## TODO（可勾选）

- [ ] `PLAN.md` → `PLAN.archive.md`，同时新建空 `PLAN.md` 写当下目标
- [ ] `data/windsurf.db` 插真账号 + `account_test.go::TestSelectAccountReturnsRawToken`
- [ ] `cmd/ls-smoke -mode=send` 跑出第一段真 token 流
- [ ] `internal/grpc/testdata/` 落 raw bytes fixture + round-trip 测试
- [ ] 对齐 Cascade cold stall 75s 的 timeout 配置
- [ ] 复核 cache_read「稳定前缀」语义在 Go 这边的实现

祝顺利。
