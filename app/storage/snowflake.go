package storage

// 雪花发号：file_key 的分配从"库里的单行计数器"改成"进程内生成的 64 位整数"。
//
// 换掉的原因是一条实测的阻塞链：旧方案在写事务里对
// <file_table>_file_key_seq 的同一行做 UPDATE next=next+1，X 锁一直持到提交，
// 而提交发生在整批多语句 MERGE 之后（17 列格式每语句 105 行，1000 行一批约
// 10 个 chunk、每 chunk 各一次 Prepare+Exec）。于是 max_parallel_sources 路
// 并发全部排在同一个热点行上——现场 sp_who2 抓到 15 个会话 LCK_M_X 等同一条
// UPDATE，等待 8.3–13.1 秒。并发在登记路径上被实质归零。
//
// 本方案零往返、零共享行：键由进程自己算，数据库只在注册表 INSERT 时看到它。
// 唯一性靠三件事叠加，都不需要协调：
//   1) 位布局给每个发号器实例 16 位节点号（65536 个空间）+ 6 位同毫秒序号。
//      发号器按**注册表**进程级单例：同一进程内，所有共享同一
//      (dialect, driver, DSN, file_table) 的 source unit 共用一个发号器。
//      注意 source unit 的数量是"机台 × 各自格式组"：现网 60 台机台按组
//      展开出约 1119 个 source unit、1119 个 TableStorage（格式声明共 23
//      个），但它们共用 1 个发号器，不是每个 format/实例各一个。节点号
//      缺省由该进程随机取一个；跨进程（多开、多机）才各有各的节点号；
//   2) 发号器初始化时用注册表 MAX(file_key) 里的时间戳做基线，新键恒大于
//      库里既有键（跨重启、跨时钟回拨都成立，且旧计数器发的小键天然不冲突）。
//      这只保证"大于既有键"，不保证两个同时初始化的进程互不相同——不同
//      节点号才提供后者；
//   3) 注册表的 (file_key) 唯一索引是最后防线——万一两个发号器同节点号同
//      毫秒同序号，这里报错而不是让两个文件共用一个键。按失败位置分三条
//      路：登记成功但数据写失败，重试复用原键；三元组先到者胜出，重试复用
//      胜出者的键；只有 file_key 唯一索引撞键这一路会在重试时换新键（进程
//      内单例之后，这一路只可能来自跨进程或人为替换发号器）。三条都不会
//      把数据挂到别的文件上，所以撞键是自愈/可重试事件，不是损坏。
//
// 不选 UUIDv7：它是 128 位，要把 file_key 从 BIGINT 换成 BINARY(16)/
// UNIQUEIDENTIFIER，主键宽度、各方言列型、以及已实测的 59.1% 体积收益
// （file_id 那 77 字节串每行存两遍 → 8 字节整型）全部重算；而 UUIDv7 相对
// 雪花并没有多给出我们需要的东西（单调 + 无协调）。

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"math/big"
	"strings"
	"sync"
	"time"

	"gocordis-csv-collector/app/errs"
)

const (
	// 位布局：41 位毫秒时间戳 | 16 位节点号 | 6 位序号，符号位恒 0，
	// 所以最大值 < 2^63，各方言的 BIGINT / NUMBER(19) / INTEGER 直接容下。
	flakeTimeBits = 41
	flakeNodeBits = 16
	flakeSeqBits  = 6

	flakeNodeShift = flakeSeqBits                 // 6
	flakeTimeShift = flakeSeqBits + flakeNodeBits // 22

	flakeNodeMax = 1<<flakeNodeBits - 1 // 65535
	flakeSeqMax  = 1<<flakeSeqBits - 1  // 63

	// 41 位毫秒 ≈ 69.7 年：纪元 2026-01-01 可用到 2095 年。
	flakeYearsUsable = 69
)

// flakeEpoch 是时间戳字段的零点。用 time.Date 现算而不是写死毫秒常量，
// 避免手算纪元出错（错一天就是 8.6e7 个键的位移）。
var flakeEpoch = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC).UnixMilli()

// flakeNow 是发号器取时的唯一入口；测试替换它来构造时钟回拨与序号耗尽。
var flakeNow = time.Now

// fileKeyGen 是一个进程内的雪花发号器。零 DB 往返，唯一的状态是
// "上一个发出的毫秒 + 该毫秒内的序号"。
type fileKeyGen struct {
	mu     sync.Mutex
	node   int64 // 16 位节点号
	lastMs int64 // 距纪元的毫秒数；恒 >= 上一个发出键的时间戳
	seq    int64
}

// newFileKeyGen 构造发号器。baselineKey 是注册表当前的 MAX(file_key)（无行
// 时传 0）：新键必须严格大于它，所以基线时间戳取 max(now, 基线键内 +1)。
// 旧计数器的键都很小（1..N），解出的时间戳是 0，因此自然退回 now。
func newFileKeyGen(node int64, baselineKey int64) *fileKeyGen {
	g := &fileKeyGen{node: node & flakeNodeMax}
	now := flakeNow().UnixMilli() - flakeEpoch
	if ts := baselineKey >> flakeTimeShift; ts+1 > now {
		now = ts + 1
	}
	g.lastMs = now
	return g
}

// next 发下一个键。单调不减，且同一进程内严格递增。
//
// 时钟回拨：不回拨时间戳字段——把 now 抬回 lastMs（冻结），在本毫秒内继续
// 加序号；序号耗尽就把 lastMs 前移 1ms（"借未来"）。代价只是键的时间戳比
// 真实时间超前几十毫秒量级，而 file_key 只需要唯一、不需要与真实时间对齐。
func (g *fileKeyGen) next() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	if now := flakeNow().UnixMilli() - flakeEpoch; now > g.lastMs {
		g.lastMs = now
		g.seq = 0
	} else if g.seq >= flakeSeqMax {
		g.lastMs++
		g.seq = 0
	} else {
		g.seq++
	}
	return (g.lastMs << flakeTimeShift) | (g.node << flakeNodeShift) | g.seq
}

// flakeNodeOf / flakeSeqOf 拆一个键的字段，供诊断与测试断言。
func flakeNodeOf(key int64) int64 { return (key >> flakeNodeShift) & flakeNodeMax }
func flakeSeqOf(key int64) int64  { return key & flakeSeqMax }

// flakeTimeOf 返回键内时间戳换算的真实时刻。旧计数器发的小键解不出意义，
// 调用方只在"看起来是雪花键"（时间戳 > 0）时使用。
func flakeTimeOf(key int64) time.Time {
	return time.UnixMilli(flakeEpoch + key>>flakeTimeShift).UTC()
}

// randomFlakeNode 取一个密码学随机的 16 位节点号。用 crypto/rand 而不是
// "主机名+PID 哈希"：同一台机器上可能有多个进程（不同配置目录、滚动升级
// 的新旧进程）共用一张注册表，哈希输入相同就会把它们绑到同一个节点号上，
// 随机至少把它们摊开。进程内部由注册表单例保证只有一个发号器，所以这里
// 不需要再为"每个 format"分摊。
func randomFlakeNode() int64 {
	n, err := rand.Int(rand.Reader, big.NewInt(flakeNodeMax+1))
	if err != nil {
		// 密码学随机源不可用是异常环境；退到时间戳低位，至少让同一时刻
		// 初始化的不同发号器取到不同值（唯一索引兜底其余）。
		return (flakeNow().UnixNano() >> 12) & flakeNodeMax
	}
	return n.Int64()
}

// fileKeyGenKey 标识"一个注册表在一个进程内的发号器"。
//
// 数据表（cfg.Table）刻意不在键里：同一注册表下的所有数据表必须共用同一个
// 发号器，否则又退回"每个 source unit 一个发号器"——现网 1119 个 source unit
// 会变成 1119 个同时存在的随机节点号。DSN 只存哈希，缓存不持有明文凭据。
type fileKeyGenKey struct {
	dialect   string
	driver    string
	fileTable string
	dsnHash   [sha256.Size]byte
}

// sharedFileKeyGen 是注册表级发号器的槽位。mu 只串行化同一注册表的初始化与
// 节点号一致性校验，不同注册表互不阻塞；gen 一旦建立就不再更换，进程内所有
// source unit 的整个生命周期都看到同一条序列。
type sharedFileKeyGen struct {
	mu     sync.Mutex
	gen    *fileKeyGen
	pinned bool  // 首个调用者显式配置了 file_key_node
	node   int64 // 实际节点号（显式值或随机值）
}

var (
	sharedGensMu sync.Mutex
	sharedGens   = map[fileKeyGenKey]*sharedFileKeyGen{}
)

// initFileKeyGen 在 schema 阶段（写事务之外）把发号器立起来：同一注册表在
// 进程内只有一个发号器，第一个调用者读注册表 MAX(file_key) 作基线，后续
// 调用者直接复用。节点号缺省按注册表随机取一个；file_key_node 因此是
// 注册表级钉值——共享同一注册表的源必须全部省略它，或全部钉同一个值。
//
// MAX 走的是注册表上的 (file_key) 唯一索引，是一次索引末端读，成本可忽略；
// 放在启动期而不是每批发号前，正是为了写路径一次往返都不发。
// sqlite 的单连接（openTuned 里 SetMaxOpenConns(1)）也要求这一步不能在写
// 事务持有的同时再发语句。
func (t *TableStorage) initFileKeyGen(ctx context.Context) error {
	if t.fileKeyGen.Load() != nil {
		return nil
	}
	g, err := t.registryFileKeyGen(ctx)
	if err != nil {
		return err
	}
	t.fileKeyGen.CompareAndSwap(nil, g)
	return nil
}

func (t *TableStorage) registryFileKeyGen(ctx context.Context) (*fileKeyGen, error) {
	// :memory: 的每个池就是另一个库（测试隔离依赖这一点），没有"同一注册表"
	// 可共享，按实例发号。
	if t.cfg.DSN == ":memory:" {
		g, _, err := t.newRegistryFileKeyGen(ctx)
		return g, err
	}
	key := fileKeyGenKey{
		dialect:   strings.ToLower(t.cfg.Dialect),
		driver:    t.cfg.Driver,
		fileTable: t.cfg.FileTable,
		dsnHash:   sha256.Sum256([]byte(t.cfg.DSN)),
	}
	sharedGensMu.Lock()
	e := sharedGens[key]
	if e == nil {
		e = &sharedFileKeyGen{}
		sharedGens[key] = e
	}
	sharedGensMu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.gen != nil {
		if t.cfg.FileKeyNode != nil {
			want := int64(*t.cfg.FileKeyNode) & flakeNodeMax
			switch {
			case !e.pinned:
				return nil, errs.Sourcef(errs.ErrInvalidConfig,
					"file key registry %q: file_key_node %d arrived after the registry generator was initialized without a pin; all sources sharing a registry must omit file_key_node or pin the same value",
					t.cfg.FileTable, want)
			case want != e.node:
				return nil, errs.Sourcef(errs.ErrInvalidConfig,
					"file key registry %q: file_key_node %d conflicts with the registry-level node %d; all sources sharing a registry must omit file_key_node or pin the same value",
					t.cfg.FileTable, want, e.node)
			}
		}
		return e.gen, nil
	}
	g, node, err := t.newRegistryFileKeyGen(ctx)
	if err != nil {
		return nil, err
	}
	e.gen, e.node = g, node
	e.pinned = t.cfg.FileKeyNode != nil
	return g, nil
}

// newRegistryFileKeyGen 读一次注册表 MAX(file_key) 作基线并构造发号器。
// 调用方负责保证同一注册表（:memory: 除外）只调用一次。
func (t *TableStorage) newRegistryFileKeyGen(ctx context.Context) (*fileKeyGen, int64, error) {
	ft := quoteIdent(t.cfg.Dialect, t.cfg.FileTable)
	var top sql.NullInt64
	if err := t.db.QueryRowContext(ctx, "SELECT MAX(file_key) FROM "+ft).Scan(&top); err != nil {
		return nil, 0, errs.ClassifyStorageError("file key baseline", err)
	}
	baseline := int64(0)
	if top.Valid {
		baseline = top.Int64
	}
	node := randomFlakeNode()
	if t.cfg.FileKeyNode != nil {
		node = int64(*t.cfg.FileKeyNode) & flakeNodeMax
	}
	return newFileKeyGen(node, baseline), node, nil
}

// nextFileKeyLocal 取本进程生成的键。发号器未初始化（存量库、schema 阶段
// 被跳过）时按需补一次——仍然是事务外调用，不会与写事务争连接。
func (t *TableStorage) nextFileKey(ctx context.Context) (int64, error) {
	g := t.fileKeyGen.Load()
	if g == nil {
		if err := t.initFileKeyGen(ctx); err != nil {
			return 0, err
		}
		g = t.fileKeyGen.Load()
	}
	if g == nil {
		return 0, errs.Sourcef(errs.ErrStoragePermanent, "file key generator unavailable for table %q", t.cfg.Table)
	}
	return g.next(), nil
}
