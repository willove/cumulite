# cumulite — CumuDB 的简易版（嵌入式）

cumulite 回答一个可以用测试证明的问题：**一个套件说"跑在 CumuDB 上"，它到底依赖什么？**
答案是 16 个端点的方法集，就是本仓库的 `Port` 接口——文档 CRUD、KV、向量 KNN、变更日志。没有
AQL、没有全文检索、没有图算子、没有时序。cumulite 把这 16 个方法用 Badger 在进程内实现，
单文件存储、无服务端、零网络。

`ask` 套件因此获得一条**零代码改动**的降级路径：

```bash
ask -lite /path/to/dir put -title T -body "..."   # 不连任何 cumudb 服务端
ask -lite /path/to/dir search -q "查询"           # 同一套集合与 KV 语义照旧
```

`-lite` 缺席时行为与从前逐字节一致（HTTP 客户端经一个边界适配器满足同一个 `Port`）。

## 与 cumudb 的关系：无依赖

两个项目互不引用：cumulite 的 `go.mod` 只有 Badger 一个依赖，没有 `replace`，没有兄弟目录
前提，`GOPROXY=off` 可完整构建与测试。契约类型住在 `contract/` 包——从
`github.com/willove/cumudb/pkg/client` **逐字段分叉**（字段名、类型、JSON tag 一致，
2026-09-23 分叉时点）。分叉而不是复用，是为了让 cumulite 的构建、发布、被 vendored 都不
需要 cumudb 在场。

代价是名义类型：`*client.Client` 的方法签名不再等于 `Port`。三方各有位：

- **ask**（双路径消费方）：默认路径仍是 HTTP 客户端，`internal/cumuport` 一个适配器把
  client 类型转成 contract 类型，只有一个调用点付这个成本；
- **Parity**：分叉漂移从"编译期报错"变成"测试可见"——`contract/contract_test.go`
  用 golden JSON 钉死每个字段，动一个字段就会红。cumudb 侧若要改契约，需要两边同步改，
  这是自觉行为而非事故；
- **复验**：需要在场复验时，在有 cumudb 副本的机器上写一个只 import 两个包的小程序，
  对 `var _ cumulite.Port = clientPort{client.New(...)}` 做一次编译期断言即可。

## 契约（`Port`）

| 模态 | 方法 |
| --- | --- |
| 文档 | `EnsureCollection` · `Insert`（批量，`_id` 定键，重复键报 `ErrDuplicate`）· `GetDocument` · `ReplaceDocument` · `PatchDocument`（`$set`）· `DeleteDocument` · `Query`（filter/skip/limit） |
| KV | `KVPut`（TTL 生效）· `KVGet`（404 语义）· `KVDelete` · `KVKeys`（prefix/limit） |
| 向量 | `CreateIndexRequest`（vector：field/dims/metric/model）· `KNN`（支持 request Filter） |
| 变更 | `SetChangelog` · `Changes`（游标续读） |
| 健康 | `Health` |

错误以 `contract.ErrNotFound` 为 sentinel（`IsNotFound` 同义），消费方保持同一个 not-found
惯用法；重复键可经 `ErrDuplicate` 分类，不再需要字符串匹配。

**刻意不做**（cumudb 有、`Port` 没有，调用即编译失败，防止顺手长出依赖）：查询语言、
混合检索、backup/export、namespace 管理、Upsert 模式族、CAS/Incr/MPut、持久化/文本/图索引族
（拒绝而非静默忽略）、时序 collection。

## 关键语义（与 cumudb 服务端对齐处）

- **写入 fail-closed**：未声明集合的 `Insert` 报 `COLLECTION_NOT_FOUND`（404），笔误不会
  静默变成新空集合——与 cumudb 实测一致（`ingest-jsonl` 不 ensure 直接灌，两边都拒）。
- **分页顺序**：`Query` 无 `Sort`，但**必须**稳定——按 Badger 键序遍历。不带稳定顺序的
  分页会重复和漏文档；`ActiveSources` 的全库翻页（14k 篇，每页 1000）依赖这一点。
- **Sort/Projection 拒绝，不静默忽略**：调用即返回 `ErrUnsupported`。lite 引擎没有查询
  规划器，悄悄丢一个 sort 比响亮失败更危险。
- **KNN Filter 生效**：`request.Filter` 在排序前过滤，被过滤的文档不占位。ask 的三处
  KNN 调用（eval-run / search / widen）都传 `status: active`——忽略它就是让 retired
  源混进排名。过滤路径不再把候选堆限制在 k（最近的 k 个可能全被过滤），改为全量打分
  排序，规模相同仍是毫秒级。
- **建索引即回填**：对已有集合 `CreateIndexRequest` 会把存量文档的向量镜像进新键空间，
  分批写（500/事务）。"先灌后建索引 = 索引扫不到任何东西"是个坑，服务端建即构建。
- **变更游标**：`Changes(cursor, limit)` 返回游标之后的记录，游标推进到"最后一条序号"；
  无新记录时保持起点游标。文档写与其变更记录在**同一个 Badger 事务**里，读者永远看不到
  半笔写入。
- **context 全部兑现**：16 个方法都接受 `ctx`，取消/超时会中止扫描（全库 Query、KNN
  扫描、KVKeys、Insert 批次、changelog 读取），返回 `ctx.Err()`。
- **距离定义**抄自服务端 `internal/vector`：cosine = `1 - cos`（相似度钳到 [-1,1]，
  零向量无角度、距离记 1）、l2、ip（取负，越小越近）。
- **KNN 是精确扫描**，无 ANN。14313×384 实测 ~78ms/问（M1 Max，内存态）——对 opt-in 的
  L1 加速路径够用；上到十万级再评估图索引。

## 规模实测（`go test -bench`，14313 篇合成语料，Apple M1 Max）

|  workload | 数值 |
| --- | --- |
| 灌库（batch 500/事务） | 378ms（0.026ms/篇） |
| `ActiveSources` 全量翻页（15 页） | 440ms |
| evidence 过滤（eq + $gte，limit 200） | 0.8ms |
| `_id $in` 500 批量 | 107ms |
| KNN 384 维 k=8 | 78ms |

## 二进制

```bash
go build -o bin/cumulite ./cmd/cumulite

bin/cumulite health -data ./store
bin/cumulite collections -data ./store
bin/cumulite doc query -data ./store -filter '{"status":"live"}' ask_sources
bin/cumulite knn -data ./store -field body_embed -vector 0.1,0.2,... -k 8 -filter '{"status":"active"}'
bin/cumulite changes -data ./store -cursor 0 ask_sources
bin/cumulite changelog -data ./store ask_sources on
bin/cumulite verify -data ./store     # 全键空间走查：逐值解码 + changelog 计数器核对
```

它是检查/运维工具，**不是服务端**：没有监听、没有协议。一旦这个二进制长出网络面，它就
开始向 cumudb 收敛，"lite" 不再有意义。

## 行为一致性证据

- `ask` hermetic 回归 `TestAskRunsOnCumuliteWithoutServer`（`cmd/ask/lite_test.go`）：
  put/ensure/ensure-embed/reconcile/session/cluster/weak-edge 全链 + 一次真实 FAST search，
  进程内没有任何 cumudb 服务端。
- 20 查询离线 A/B（同 60 篇语料，cumudb:8594 vs cumulite）：样本 id、confidence、coverage
  **20/20 一致**；top 样本 business_key **20/20 一致**；gold top-1 双双 2/20（离线词面打分
  的真实水平，与存储无关）。原始数据 `ask/var/goldeval-cumulite/ab.json`。
- cumulite 自身契约测试：重复插入（`ErrDuplicate` 分类）、patch 算子面、`$or`/`$in` 的 Go 字面量
  与 JSON 两种分片形态、跳过边界、TTL 过期、余弦排序与零向量、changelog 游标与幂等、
  KNN Filter、Sort/Projection 拒绝、ctx 取消、建索引回填与回填维度不符。

## 对 ask 的接线

`ingest.Store` / `cluster.CumuStore` / `graph.CumuStore` / `deep.Cumu{Store,CiteStore}` 的
存储字段类型是 `cumulite.Port`；CLI 管线（search/serve/eval-run/session/cluster/conflicts）
同型。`-lite DIR` 打开 Badger 引擎，HTTP 路径经 `internal/cumuport` 适配器，其余装配不变。
**这不改变 ask 对 cumudb 的默认依赖**——生产仍是 HTTP 客户端；cumulite 是把"存储可移植"
从架构意图变成可执行证据的那一步。

## 运维备注

- 默认不同步 fsync（Badger value log 扛进程死亡；机器崩溃最多丢尾部写入）。要强持久：
  `cumulite.WithSyncWrites()`。
- Badger 编译/压缩有后台 IO；长驻进程建议监控 `DB()` 上的 LSM 状态（cumudb 的 compaction
  经验同样适用）。
- 命名空间不在引擎内：`ask` 侧 `ns.Coll` 已把 `ns:coll` 合成进集合名，引擎把集合名当不透明
  字节串（含 `:` 合法，含 NUL 拒绝）。
