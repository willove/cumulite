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

`-lite` 缺席时行为与从前逐字节一致（`*client.Client` 满足同一个 `Port`）。

## 契约（`Port`）

| 模态 | 方法 |
| --- | --- |
| 文档 | `EnsureCollection` · `Insert`（批量，`_id` 定键，重复键报错）· `GetDocument` · `ReplaceDocument` · `PatchDocument`（`$set`）· `DeleteDocument` · `Query`（filter/skip/limit） |
| KV | `KVPut`（TTL 生效）· `KVGet`（404 语义）· `KVDelete` · `KVKeys`（prefix/limit） |
| 向量 | `CreateIndexRequest`（vector：field/dims/metric/model）· `KNN` |
| 变更 | `SetChangelog` · `Changes`（游标续读） |
| 健康 | `Health` |

方法签名与 `github.com/willove/cumudb/pkg/client` 逐字段一致（类型也直接复用它），所以：
`var _ Port = (*client.Client)(nil)` 在编译期成立——客户端就是契约的超集，引擎是实现。
错误对 `client.IsNotFound` 为真（404 语义），消费方保持同一个 not-found 惯用法。

**刻意不做**（cumudb 有、`Port` 没有，调用即编译失败，防止顺手长出依赖）：查询语言、
混合检索、backup/export、namespace 管理、Upsert 模式族、CAS/Incr/MPut、持久化/文本/图索引族
（拒绝而非静默忽略）、时序 collection。

## 关键语义（与 cumudb 服务端对齐处）

- **写入 fail-closed**：未声明集合的 `Insert` 报 `COLLECTION_NOT_FOUND`（404），笔误不会
  静默变成新空集合——与 cumudb 实测一致（`ingest-jsonl` 不 ensure 直接灌，两边都拒）。
- **分页顺序**：`Query` 无 `Sort`，但**必须**稳定——按 Badger 键序遍历。不带稳定顺序的
  分页会重复和漏文档；`ActiveSources` 的全库翻页（14k 篇，每页 1000）依赖这一点。
- **变更游标**：`Changes(cursor, limit)` 返回游标之后的记录，游标推进到"最后一条序号"；
  无新记录时保持起点游标。文档写与其变更记录在**同一个 Badger 事务**里，读者永远看不到
  半笔写入。
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

## 行为一致性证据

- `ask` hermetic 回归 `TestAskRunsOnCumuliteWithoutServer`（`cmd/ask/lite_test.go`）：
  put/ensure/ensure-embed/reconcile/session/cluster/weak-edge 全链 + 一次真实 FAST search，
  进程内没有任何 cumudb 服务端。
- 20 查询离线 A/B（同 60 篇语料，cumudb:8594 vs cumulite）：样本 id、confidence、coverage
  **20/20 一致**；top 样本 business_key **20/20 一致**；gold top-1 双双 2/20（离线词面打分
  的真实水平，与存储无关）。原始数据 `ask/var/goldeval-cumulite/ab.json`。
- cumulite 自身契约测试（`engine_test.go`）：重复插入、patch 算子面、`$or`/`$in` 的 Go 字面量
  与 JSON 两种分片形态、跳过边界、TTL 过期、余弦排序与零向量、changelog 游标与幂等。

## 对 ask 的接线

`ingest.Store` / `cluster.CumuStore` / `graph.CumuStore` / `deep.Cumu{Store,CiteStore}` 的
存储字段类型是 `cumulite.Port`；CLI 管线（search/serve/eval-run/session/cluster/conflicts）
同型。`-lite DIR` 打开 Badger 引擎，其余装配不变。**这不改变 ask 对 cumudb 的默认依赖**——
生产仍是 HTTP 客户端；cumulite 是把"存储可移植"从架构意图变成可执行证据的那一步。

## 运维备注

- 默认不同步 fsync（Badger value log 扛进程死亡；机器崩溃最多丢尾部写入）。要强持久：
  `cumulite.WithSyncWrites()`。
- Badger 编译/压缩有后台 IO；长驻进程建议监控 `DB()` 上的 LSM 状态（cumudb 的 compaction
  经验同样适用）。
- 命名空间不在引擎内：`ask` 侧 `ns.Coll` 已把 `ns:coll` 合成进集合名，引擎把集合名当不透明
  字节串（含 `:` 合法，含 NUL 拒绝）。
