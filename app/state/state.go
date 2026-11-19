package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gocordis-csv-collector/app/model"
)

type collectionRecord struct {
	Key       model.CollectionKey
	Status    model.Status
	StartedAt time.Time
	EndedAt   time.Time
	Note      string
}

type fileRecord struct {
	Key         model.CollectionKey
	File        model.FileIdentity
	Records     int64
	CompletedAt time.Time
}

func stateKey(k model.CollectionKey) string {
	return string(k.SourceID) + "|" + k.Date.String()
}

// interruptedNote 是启动回收写入的原因：它让控制台的"原因"列直接说明
// Running 为什么结束，而不是留一条无解释的失败。
const interruptedNote = "collection interrupted by process restart"

// MemoryState is an in-process CollectionState. It is safe for concurrent use
// and supports stale Running leases for process-restart recovery tests.
type MemoryState struct {
	mu          sync.Mutex
	collections map[string]collectionRecord
	files       map[string]map[string]fileRecord
	// failedFiles is the local failure ledger: one record per failed file per
	// collection, cleared when the file later completes.
	failedFiles map[string]map[string]failedFileRecord
	// checks holds the latest consistency verdict per (collection, file). Like
	// the failure ledger it is evidence, not work: nothing retries it.
	checks map[string]map[string]model.FileCheck
	// appendMode disables the collection-level succeeded guard: every trigger
	// re-opens the collection (watch/change-triggered sources record many
	// snapshots under one business date). File-level dedup still applies.
	appendMode bool
	now        func() time.Time
	// noData 是"共享可达但该业务日探不到文件"的连续计数，按业务日分开。
	// 它是无数据确认（collector 的 no_data_after_days）的证据：落台账、
	// 重启不清零——否则每天重启的采集器永远凑不满 N 次。
	noData map[string]*noDataStreak
}

type noDataStreak struct {
	Consecutive int
	LastAt      time.Time
}

// failedFileRecord is the persistent evidence of one failed file attempt.
type failedFileRecord struct {
	Key           model.CollectionKey
	File          model.FileIdentity
	Error         string
	FailedAt      time.Time
	Attempts      int
	NextAttemptAt time.Time
}

func (r *failedFileRecord) toView() model.FileFailure {
	return model.FileFailure{Key: r.Key, File: r.File, Error: r.Error, FailedAt: r.FailedAt, Attempts: r.Attempts, NextAttemptAt: r.NextAttemptAt}
}

func NewMemory() *MemoryState {
	return &MemoryState{
		collections: make(map[string]collectionRecord),
		files:       make(map[string]map[string]fileRecord),
		failedFiles: make(map[string]map[string]failedFileRecord),
		checks:      make(map[string]map[string]model.FileCheck),
		noData:      make(map[string]*noDataStreak),
	}
}

// SetAppendMode switches the collection-level guard off: every trigger
// re-opens a succeeded collection (change-triggered sources).
func (s *MemoryState) SetAppendMode() { s.appendMode = true }

func (s *MemoryState) nowTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *MemoryState) Begin(ctx context.Context, key model.CollectionKey, lease time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowTime()
	ck := stateKey(key)
	if rec, ok := s.collections[ck]; ok {
		switch rec.Status {
		case model.StatusSucceeded:
			if !s.appendMode {
				return false, nil
			}
		case model.StatusRunning:
			if now.Sub(rec.StartedAt) < lease {
				return false, nil
			}
		}
	}
	s.collections[ck] = collectionRecord{Key: key, Status: model.StatusRunning, StartedAt: now}
	return true, nil
}

func (s *MemoryState) End(ctx context.Context, key model.CollectionKey, status model.Status, note string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ck := stateKey(key)
	rec, ok := s.collections[ck]
	if !ok {
		return fmt.Errorf("collection state %q has not begun", key)
	}
	if rec.Status == model.StatusRunning && rec.StartedAt.IsZero() {
		rec.StartedAt = s.nowTime()
	}
	rec.Status = status
	rec.Note = note
	rec.EndedAt = s.nowTime()
	s.collections[ck] = rec
	// 成功实例没有失败账：同路径完成已在 MarkFileCompleted 退役对应条目，
	// 此时仍残留的失败只可能属于本轮清单里已不存在的文件（上游已删除）。
	// 保留它们会让投影在成功实例上数出失败文件，且成功守卫使其实永无
	// 清理机会——成功即清账。
	if status == model.StatusSucceeded || status == model.StatusSkipped {
		// 成功清账；确认无数据同样清账——日期已结算，残留的失败条目没有
		// 任何补救路径（这个日期不会再被自动重试），只会永久抬高
		// FilesFailed/趋势的失败计数。审计线索在观察流 journal 里。
		delete(s.failedFiles, ck)
	}
	return nil
}

// migratePreInstance 迁移旧语义记录（仅限构造期调用，实例尚未发布）：
//   - Skipped → 原样保留——它从"旧语义遗留"重新成为 Iteration C 的合法
//     终态（确认无数据），装载清除会让确认结果抹掉、重启后回到每晚重探；
//   - Pending（等待数据）→ 转 Failed——它是重试证据，转换后仍可被
//     补采重探（迟到文件由成功覆盖），而不是静默消失；
//   - Succeeded 记录带失败账 → 清失败账（成功清账不变量的修复：
//     旧版本在文件从清单消失后留下孤儿失败条目，投影会在成功实例
//     上数出失败文件）。
//
// 返回（清除数, 转换数）。
func (s *MemoryState) migratePreInstance() (purged, converted int) {
	for k, rec := range s.collections {
		switch rec.Status {
		// Skipped 曾是"旧语义遗留、装载即清"的状态；Iteration C 让
		// "确认无数据"重新落成 Skipped 终态后，装载清除会把确认结果抹掉
		// ——重启一次就回到每晚重探，文档承诺的"不再消耗探针"失效。
		// Skipped 现在原样保留（两个来源语义一致：都是"已结算的无数据"）。
		case model.StatusSkipped:
			// 保留
		case model.StatusPending:
			rec.Status = model.StatusFailed
			s.collections[k] = rec
			converted++
		case model.StatusSucceeded:
			if len(s.failedFiles[k]) > 0 {
				delete(s.failedFiles, k)
				purged++
			}
		}
	}
	return purged, converted
}

// Drop 移除一个采集键的全部台账（实例制：无证据不物化）。连带清理
// 该键的文件记录与失败台账——否则留下"采集记录不存在但失败账还在"
// 的孤儿，文件重新出现时也会被已完成门误挡。
func (s *MemoryState) Drop(ctx context.Context, key model.CollectionKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ck := stateKey(key)
	delete(s.collections, ck)
	delete(s.files, ck)
	delete(s.failedFiles, ck)
	delete(s.checks, ck)
	// 注意：noData 计数不在这里清——它是探针证据（"这个日期被探到过没有"），
	// 不是台账行。清它的只有两个入口：文件出现（ResetNoData）与从未开启确认。
	return nil
}

func (s *MemoryState) FileCompleted(ctx context.Context, key model.CollectionKey, file model.FileIdentity) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ck := stateKey(key)
	if _, ok := s.collections[ck]; !ok {
		return false, nil
	}
	rec, ok := s.files[ck][file.Identity()]
	return ok && rec.File.Identity() == file.Identity(), nil
}

func (s *MemoryState) MarkFileCompleted(ctx context.Context, key model.CollectionKey, file model.FileIdentity, records int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ck := stateKey(key)
	if _, ok := s.collections[ck]; !ok {
		return fmt.Errorf("collection state %q has not begun", key)
	}
	if s.files[ck] == nil {
		s.files[ck] = make(map[string]fileRecord)
	}
	s.files[ck][file.Identity()] = fileRecord{Key: key, File: file, Records: records, CompletedAt: s.nowTime()}
	// A completed file retires its failure records for the same path: a
	// corrected re-export has a new identity, and the ledger holds exactly
	// the paths a later trigger should still worry about.
	for id, rec := range s.failedFiles[ck] {
		if rec.File.Path == file.Path {
			delete(s.failedFiles[ck], id)
		}
	}
	return nil
}

// MarkFileFailed persists one failed-file attempt. The first failure creates
// the record; later attempts increment its counter and refresh the error, so
// the ledger shows how often a file has failed and why most recently.
func (s *MemoryState) MarkFileFailed(ctx context.Context, key model.CollectionKey, file model.FileIdentity, errMsg string, throttled bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ck := stateKey(key)
	if s.failedFiles[ck] == nil {
		s.failedFiles[ck] = make(map[string]failedFileRecord)
	}
	rec, ok := s.failedFiles[ck][file.Identity()]
	if !ok {
		rec = failedFileRecord{Key: key, File: file}
	}
	rec.Error = errMsg
	rec.FailedAt = s.nowTime()
	rec.Attempts++
	if throttled {
		rec.NextAttemptAt = model.NextAttempt(rec.Attempts, rec.FailedAt)
	} else {
		// 基础设施失败只记账不节流：恢复的信号是基础设施本身回来了。
		rec.NextAttemptAt = time.Time{}
	}
	s.failedFiles[ck][file.Identity()] = rec
	return nil
}

// ReclaimRunning closes every Running record started before startedBefore as
// an interrupted failure. It is called once at process start with the host's
// construction time: rows from the previous process are necessarily orphaned
// (single-instance lock), while runs started by this process stay untouched.
func (s *MemoryState) ReclaimRunning(ctx context.Context, startedBefore time.Time) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowTime()
	var n int64
	for k, rec := range s.collections {
		if rec.Status != model.StatusRunning || !rec.StartedAt.Before(startedBefore) {
			continue
		}
		rec.Status = model.StatusFailed
		rec.Note = interruptedNote
		rec.EndedAt = now
		s.collections[k] = rec
		n++
	}
	return n, nil
}

// CollectionRecords returns every persisted collection record of one source,
// oldest business date first.
func (s *MemoryState) CollectionRecords(ctx context.Context, sourceID model.SourceID) ([]model.CollectionRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.CollectionRecord, 0, len(s.collections))
	for _, rec := range s.collections {
		if rec.Key.SourceID != sourceID {
			continue
		}
		out = append(out, model.CollectionRecord{
			Key: rec.Key, Status: rec.Status, StartedAt: rec.StartedAt, EndedAt: rec.EndedAt, Note: rec.Note,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key.Date.Before(out[j].Key.Date) })
	return out, nil
}

// FileRecords returns the persisted completed-file records of one collection,
// ordered by path.
func (s *MemoryState) FileRecords(ctx context.Context, key model.CollectionKey) ([]model.FileRecordView, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ck := stateKey(key)
	out := make([]model.FileRecordView, 0, len(s.files[ck]))
	for _, rec := range s.files[ck] {
		out = append(out, model.FileRecordView{Key: rec.Key, File: rec.File, Records: rec.Records, CompletedAt: rec.CompletedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File.Path < out[j].File.Path })
	return out, nil
}

// RecordSources lists the sources that have persisted records, sorted.
func (s *MemoryState) RecordSources(ctx context.Context) ([]model.SourceID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[model.SourceID]struct{}{}
	var out []model.SourceID
	for _, rec := range s.collections {
		if _, ok := seen[rec.Key.SourceID]; ok {
			continue
		}
		seen[rec.Key.SourceID] = struct{}{}
		out = append(out, rec.Key.SourceID)
	}
	for ck := range s.files {
		for _, rec := range s.files[ck] {
			if _, ok := seen[rec.Key.SourceID]; ok {
				continue
			}
			seen[rec.Key.SourceID] = struct{}{}
			out = append(out, rec.Key.SourceID)
		}
	}
	sort.Slice(out, func(i, j int) bool { return string(out[i]) < string(out[j]) })
	return out, nil
}

// ListFileFailures returns the persisted failure records of one source,
// oldest failure first.
func (s *MemoryState) ListFileFailures(ctx context.Context, sourceID model.SourceID) ([]model.FileFailure, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.FileFailure
	for _, m := range s.failedFiles {
		for _, rec := range m {
			if rec.Key.SourceID != sourceID {
				continue
			}
			out = append(out, rec.toView())
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FailedAt.Equal(out[j].FailedAt) {
			return out[i].File.Path < out[j].File.Path
		}
		return out[i].FailedAt.Before(out[j].FailedAt)
	})
	return out, nil
}

func (s *MemoryState) LastCompleted(ctx context.Context, sourceID model.SourceID, before model.CollectionDate) (model.CollectionDate, bool, error) {
	if err := ctx.Err(); err != nil {
		return model.CollectionDate{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var last model.CollectionDate
	found := false
	for _, rec := range s.collections {
		if rec.Key.SourceID != sourceID || rec.Status != model.StatusSucceeded {
			continue
		}
		if !rec.Key.Date.After(before) && (!found || last.Before(rec.Key.Date)) {
			last = rec.Key.Date
			found = true
		}
	}
	return last, found, nil
}

// StatusOf returns the persisted status of one collection key.
func (s *MemoryState) StatusOf(_ context.Context, key model.CollectionKey) (model.Status, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.collections[stateKey(key)]
	if !ok {
		return "", false, nil
	}
	return rec.Status, true, nil
}

// ExpireBeyond 把已经离开自动窗口的未完成记录转成 Expired：记录一条不删，
// 只是不再自动重试。Succeeded/Skipped/Expired 原样不动——它们是事实，不是活。
func (s *MemoryState) ExpireBeyond(ctx context.Context, sourceID model.SourceID, before model.CollectionDate) ([]model.CollectionKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var changed []model.CollectionKey
	for ck, rec := range s.collections {
		if rec.Key.SourceID != sourceID || !rec.Key.Date.Before(before) {
			continue
		}
		switch rec.Status {
		case model.StatusFailed, model.StatusPending:
		default:
			continue
		}
		rec.Status = model.StatusExpired
		rec.Note = "自动补采窗口外，停止自动重试；需按日期手动回填"
		s.collections[ck] = rec
		changed = append(changed, rec.Key)
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].Date.Before(changed[j].Date) })
	return changed, nil
}

// RecordNoDataProbe 见 model.CollectionState：按业务日累加，达到阈值后持续
// 返回 true（终态化是幂等的）。
func (s *MemoryState) RecordNoDataProbe(ctx context.Context, key model.CollectionKey, confirmAfter int) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if confirmAfter <= 0 {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ck := stateKey(key)
	st := s.noData[ck]
	if st == nil {
		st = &noDataStreak{}
		s.noData[ck] = st
	}
	st.Consecutive++
	st.LastAt = s.nowTime()
	return st.Consecutive >= confirmAfter, nil
}

func (s *MemoryState) ResetNoData(ctx context.Context, key model.CollectionKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.noData, stateKey(key))
	return nil
}

func (s *MemoryState) ListIncomplete(ctx context.Context, sourceID model.SourceID, until model.CollectionDate, staleAfter time.Duration) ([]model.CollectionKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowTime()
	var out []model.CollectionKey
	for _, rec := range s.collections {
		// Succeeded and Skipped are not work: succeeded is done, skipped is
		// a checked-and-absent past day (terminal for the task list). The
		// planner re-derives calendar gaps each run anyway, so genuinely
		// late deliveries inside the window are still attempted — they just
		// never appear as "plan" tasks while absent.
		if rec.Key.SourceID != sourceID ||
			rec.Status == model.StatusSucceeded ||
			rec.Status == model.StatusSkipped ||
			rec.Status == model.StatusExpired {
			continue
		}
		if rec.Key.Date.After(until) {
			continue
		}
		if rec.Status == model.StatusRunning && now.Sub(rec.StartedAt) < staleAfter {
			continue
		}
		out = append(out, rec.Key)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date.Equal(out[j].Date) {
			return string(out[i].SourceID) < string(out[j].SourceID)
		}
		return out[i].Date.Before(out[j].Date)
	})
	return out, nil
}

func (s *MemoryState) Close() error { return nil }

type memorySnapshot struct {
	Collections []collectionRecordJSON
	Files       []fileRecordJSON
	FailedFiles []failedFileRecordJSON
	Checks      []fileCheckRecordJSON
	NoData      []noDataStreakJSON
}

type noDataStreakJSON struct {
	SourceID    model.SourceID
	Date        model.CollectionDate
	Consecutive int
	LastAt      time.Time
}

type fileCheckRecordJSON struct {
	SourceID   model.SourceID
	Date       model.CollectionDate
	Path       string
	Name       string
	Size       int64
	ModTime    time.Time
	Hash       string
	Status     model.CheckStatus
	FileRows   int64
	ParsedRows int64
	DBRows     int64
	FileCols   int
	StoredCols int
	RaggedRows int64
	Unmapped   []string
	Note       string
	CheckedAt  time.Time
}

type failedFileRecordJSON struct {
	SourceID      model.SourceID
	Date          model.CollectionDate
	Path          string
	Name          string
	Size          int64
	ModTime       time.Time
	Hash          string
	Error         string
	FailedAt      time.Time
	Attempts      int
	NextAttemptAt time.Time
}

type collectionRecordJSON struct {
	SourceID  model.SourceID
	Date      model.CollectionDate
	Status    model.Status
	StartedAt time.Time
	EndedAt   time.Time
	Note      string
}

type fileRecordJSON struct {
	SourceID    model.SourceID
	Date        model.CollectionDate
	Path        string
	Name        string
	Size        int64
	ModTime     time.Time
	Hash        string
	Records     int64
	CompletedAt time.Time
}

func snapshot(s *MemoryState) memorySnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := memorySnapshot{}
	for _, rec := range s.collections {
		out.Collections = append(out.Collections, collectionRecordJSON{
			SourceID:  rec.Key.SourceID,
			Date:      rec.Key.Date,
			Status:    rec.Status,
			StartedAt: rec.StartedAt,
			EndedAt:   rec.EndedAt,
			Note:      rec.Note,
		})
	}
	sort.Slice(out.Collections, func(i, j int) bool {
		return stateKey(model.CollectionKey{SourceID: out.Collections[i].SourceID, Date: out.Collections[i].Date}) <
			stateKey(model.CollectionKey{SourceID: out.Collections[j].SourceID, Date: out.Collections[j].Date})
	})
	for _, m := range s.files {
		for _, rec := range m {
			out.Files = append(out.Files, fileRecordJSON{
				SourceID:    rec.Key.SourceID,
				Date:        rec.Key.Date,
				Path:        rec.File.Path,
				Name:        rec.File.Name,
				Size:        rec.File.Size,
				ModTime:     rec.File.ModTime,
				Hash:        rec.File.Hash,
				Records:     rec.Records,
				CompletedAt: rec.CompletedAt,
			})
		}
	}
	sort.Slice(out.Files, func(i, j int) bool {
		return out.Files[i].Path < out.Files[j].Path
	})
	for _, m := range s.failedFiles {
		for _, rec := range m {
			out.FailedFiles = append(out.FailedFiles, failedFileRecordJSON{
				SourceID:      rec.Key.SourceID,
				Date:          rec.Key.Date,
				Path:          rec.File.Path,
				Name:          rec.File.Name,
				Size:          rec.File.Size,
				ModTime:       rec.File.ModTime,
				Hash:          rec.File.Hash,
				Error:         rec.Error,
				FailedAt:      rec.FailedAt,
				Attempts:      rec.Attempts,
				NextAttemptAt: rec.NextAttemptAt,
			})
		}
	}
	sort.Slice(out.FailedFiles, func(i, j int) bool {
		return out.FailedFiles[i].FailedAt.Before(out.FailedFiles[j].FailedAt)
	})
	for _, m := range s.checks {
		for _, rec := range m {
			out.Checks = append(out.Checks, fileCheckRecordJSON{
				SourceID: rec.Key.SourceID,
				Date:     rec.Key.Date,
				Path:     rec.File.Path,
				Name:     rec.File.Name,
				Size:     rec.File.Size,
				ModTime:  rec.File.ModTime,
				Hash:     rec.File.Hash,
				Status:   rec.Status,
				FileRows: rec.FileRows, ParsedRows: rec.ParsedRows, DBRows: rec.DBRows,
				FileCols: rec.FileCols, StoredCols: rec.StoredCols, RaggedRows: rec.RaggedRows,
				Unmapped: rec.Unmapped, Note: rec.Note, CheckedAt: rec.CheckedAt,
			})
		}
	}
	sort.Slice(out.Checks, func(i, j int) bool {
		if !out.Checks[i].Date.Equal(out.Checks[j].Date) {
			return out.Checks[i].Date.Before(out.Checks[j].Date)
		}
		return out.Checks[i].Path < out.Checks[j].Path
	})
	for ck, st := range s.noData {
		parts := strings.SplitN(ck, "|", 2)
		if len(parts) != 2 {
			continue
		}
		d, err := parseCollectionDate(parts[1])
		if err != nil {
			continue
		}
		out.NoData = append(out.NoData, noDataStreakJSON{
			SourceID: model.SourceID(parts[0]), Date: d,
			Consecutive: st.Consecutive, LastAt: st.LastAt,
		})
	}
	sort.Slice(out.NoData, func(i, j int) bool {
		return out.NoData[i].Date.Before(out.NoData[j].Date)
	})
	return out
}

func restore(s *MemoryState, snap memorySnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collections = make(map[string]collectionRecord, len(snap.Collections))
	for _, c := range snap.Collections {
		k := model.CollectionKey{SourceID: c.SourceID, Date: c.Date}
		s.collections[stateKey(k)] = collectionRecord{Key: k, Status: c.Status, StartedAt: c.StartedAt, EndedAt: c.EndedAt, Note: c.Note}
	}
	s.files = make(map[string]map[string]fileRecord)
	for _, f := range snap.Files {
		k := model.CollectionKey{SourceID: f.SourceID, Date: f.Date}
		file := model.FileIdentity{SourceID: f.SourceID, Path: f.Path, Name: f.Name, Size: f.Size, ModTime: f.ModTime, Hash: f.Hash}
		ck := stateKey(k)
		if s.files[ck] == nil {
			s.files[ck] = make(map[string]fileRecord)
		}
		s.files[ck][file.Identity()] = fileRecord{Key: k, File: file, Records: f.Records, CompletedAt: f.CompletedAt}
	}
	s.failedFiles = make(map[string]map[string]failedFileRecord)
	for _, f := range snap.FailedFiles {
		k := model.CollectionKey{SourceID: f.SourceID, Date: f.Date}
		file := model.FileIdentity{SourceID: f.SourceID, Path: f.Path, Name: f.Name, Size: f.Size, ModTime: f.ModTime, Hash: f.Hash}
		ck := stateKey(k)
		if s.failedFiles[ck] == nil {
			s.failedFiles[ck] = make(map[string]failedFileRecord)
		}
		s.failedFiles[ck][file.Identity()] = failedFileRecord{
			Key: k, File: file, Error: f.Error, FailedAt: f.FailedAt, Attempts: f.Attempts,
			NextAttemptAt: f.NextAttemptAt,
		}
	}
	s.checks = make(map[string]map[string]model.FileCheck)
	for _, c := range snap.Checks {
		k := model.CollectionKey{SourceID: c.SourceID, Date: c.Date}
		file := model.FileIdentity{SourceID: c.SourceID, Path: c.Path, Name: c.Name, Size: c.Size, ModTime: c.ModTime, Hash: c.Hash}
		ck := stateKey(k)
		if s.checks[ck] == nil {
			s.checks[ck] = make(map[string]model.FileCheck)
		}
		s.checks[ck][file.Identity()] = model.FileCheck{
			Key: k, File: file, Status: c.Status,
			FileRows: c.FileRows, ParsedRows: c.ParsedRows, DBRows: c.DBRows,
			FileCols: c.FileCols, StoredCols: c.StoredCols, RaggedRows: c.RaggedRows,
			Unmapped: c.Unmapped, Note: c.Note, CheckedAt: c.CheckedAt,
		}
	}
	s.noData = make(map[string]*noDataStreak, len(snap.NoData))
	for _, nd := range snap.NoData {
		s.noData[stateKey(model.CollectionKey{SourceID: nd.SourceID, Date: nd.Date})] = &noDataStreak{
			Consecutive: nd.Consecutive, LastAt: nd.LastAt,
		}
	}
}

// FileState persists a MemoryState snapshot to an atomic local file after every
// mutation. It is process-persistent but not multi-process safe.
type FileState struct {
	MemoryState
	path string
	mu   sync.Mutex
}

func NewFile(path string) (*FileState, error) {
	if path == "" {
		return nil, errors.New("state file path is empty")
	}
	fs := &FileState{MemoryState: *NewMemory(), path: path}
	// 只读属性会让 Windows 的 MoveFileEx 无法覆盖目标（Access is denied），
	// 且一次被设上就永远写不进去——加载时先自愈。
	clearReadOnly(path)
	if data, err := os.ReadFile(path); err == nil {
		var snap memorySnapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			return nil, fmt.Errorf("state file %q: %w", path, err)
		}
		restore(&fs.MemoryState, snap)
		// 加载即迁移：Skipped（无数据）不是实例，清除；Pending（等待
		// 数据）是重试证据，转换为 Failed——它仍可被补采重探，文件晚到
		// 由成功覆盖。迁移后回写压实，状态文件里只剩真实实例。
		if purged, converted := fs.MemoryState.migratePreInstance(); purged+converted > 0 {
			if err := fs.save(); err != nil {
				return nil, fmt.Errorf("state file %q: compact: %w", path, err)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("state file %q: %w", path, err)
	}
	return fs, nil
}

// clearReadOnly 清除文件只读位：Windows 的 MoveFileEx 无法覆盖只读目标
// （ERROR_ACCESS_DENIED）。Go 在 Windows 上把 os.Chmod 的写位映射到
// FILE_ATTRIBUTE_READONLY。
func clearReadOnly(path string) {
	if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o200 == 0 {
		_ = os.Chmod(path, 0o666)
	}
}

var tmpSeq atomic.Uint64

func (s *FileState) save() error {
	data, err := json.Marshal(snapshot(&s.MemoryState))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	// 固定 ".tmp" 会让热重载重叠或双开的两个写者互踩，每次用唯一名。
	tmp := fmt.Sprintf("%s.%d.%d.tmp", s.path, os.Getpid(), tmpSeq.Add(1))
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	clearReadOnly(s.path)
	var lastErr error
	for attempt := 0; attempt < 8; attempt++ {
		lastErr = os.Rename(tmp, s.path)
		if lastErr == nil {
			return nil
		}
		if !renameRetryable(lastErr) {
			break
		}
		// 杀软/备份代理可能在新文件写入后短暂持有句柄，退避后再试。
		clearReadOnly(s.path)
		time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
	}
	_ = os.Remove(tmp)
	return lastErr
}

// run 是一次"改内存 + 落盘"的原子动作，FileState 的每个写方法都只经过它。
// 两条规则，都是现场踩过的：
//
//	锁不许跑到 save 前面。两个写者各自 marshal 一份快照、各写自己的临时
//	文件、各自 rename 覆盖目标——rename 的先后完全由调度决定，谁在锁外落盘，
//	谁就可能用旧快照盖掉别人刚写完的新快照。
//
//	落盘失败必须回滚内存。调用方拿到错误、内存却已经改完，就会留下一条磁盘
//	上从不存在的记录：Running 会让 24 小时租约挡住这个业务日的全部重试（要
//	等进程重启才恢复），Succeeded 会让 planner 不再排它、让 Begin 的成功守卫
//	拒绝重开——一次写失败就此把一个待采日期冻死。
//
// apply 报告 changed=false 表示内存没动（Begin 没抢到、回收没找到孤儿行），
// 那就不必落盘——与改造前一致。
func (s *FileState) run(apply func() (changed bool, count int64, err error)) (bool, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	before, err := json.Marshal(snapshot(&s.MemoryState))
	if err != nil {
		return false, 0, err
	}
	changed, count, err := apply()
	if err != nil {
		return false, 0, err
	}
	if !changed {
		return false, count, nil
	}
	if err := s.save(); err != nil {
		var snap memorySnapshot
		if jErr := json.Unmarshal(before, &snap); jErr != nil {
			return false, 0, errors.Join(err, jErr)
		}
		restore(&s.MemoryState, snap)
		return false, 0, err
	}
	return true, count, nil
}

// transact adapts a mutation with no return value.
func (s *FileState) transact(apply func() error) error {
	_, _, err := s.run(func() (bool, int64, error) { return true, 0, apply() })
	return err
}

func (s *FileState) Begin(ctx context.Context, key model.CollectionKey, lease time.Duration) (bool, error) {
	claimed, _, err := s.run(func() (bool, int64, error) {
		ok, err := s.MemoryState.Begin(ctx, key, lease)
		return ok, 0, err
	})
	return claimed, err
}

func (s *FileState) End(ctx context.Context, key model.CollectionKey, status model.Status, note string) error {
	return s.transact(func() error { return s.MemoryState.End(ctx, key, status, note) })
}

func (s *FileState) Drop(ctx context.Context, key model.CollectionKey) error {
	return s.transact(func() error { return s.MemoryState.Drop(ctx, key) })
}

// SetAppendMode 没有返回值可传递失败（接口如此）：落盘失败留给下一次写入压实。
func (s *FileState) SetAppendMode() {
	_ = s.transact(func() error { s.MemoryState.SetAppendMode(); return nil })
}

func (s *FileState) MarkFileCompleted(ctx context.Context, key model.CollectionKey, file model.FileIdentity, records int64) error {
	return s.transact(func() error {
		return s.MemoryState.MarkFileCompleted(ctx, key, file, records)
	})
}

func (s *FileState) MarkFileFailed(ctx context.Context, key model.CollectionKey, file model.FileIdentity, errMsg string, throttled bool) error {
	return s.transact(func() error {
		return s.MemoryState.MarkFileFailed(ctx, key, file, errMsg, throttled)
	})
}

// ExpireBeyond 必须在这里也过一遍 run()：MemoryState 的同名方法是改内存的，
// 直接用嵌入版本会让退役只活在内存里、重启就回到 Failed——那是 U2 修过的同一类
// 错误，换了个方法名而已。
func (s *FileState) ExpireBeyond(ctx context.Context, sourceID model.SourceID, before model.CollectionDate) ([]model.CollectionKey, error) {
	var changed []model.CollectionKey
	_, _, err := s.run(func() (bool, int64, error) {
		keys, err := s.MemoryState.ExpireBeyond(ctx, sourceID, before)
		if err != nil {
			return false, 0, err
		}
		changed = keys
		return len(keys) > 0, int64(len(keys)), nil
	})
	if err != nil {
		return nil, err
	}
	return changed, nil
}

// RecordNoDataProbe/ResetNoData 必须在这里过 run()：MemoryState 的同名方法只改
// 内存，直接用嵌入版本会让"连续探不到"的证据重启清零——每天重启的采集器
// 永远凑不满 N 次，确认机制形同虚设。
func (s *FileState) RecordNoDataProbe(ctx context.Context, key model.CollectionKey, confirmAfter int) (bool, error) {
	var confirmed bool
	_, _, err := s.run(func() (bool, int64, error) {
		ok, err := s.MemoryState.RecordNoDataProbe(ctx, key, confirmAfter)
		if err != nil {
			return false, 0, err
		}
		confirmed = ok
		// 只要计了数就落盘（confirmAfter<=0 时不会改内存，不落盘）。
		return confirmAfter > 0, 0, nil
	})
	return confirmed, err
}

func (s *FileState) ResetNoData(ctx context.Context, key model.CollectionKey) error {
	return s.transact(func() error { return s.MemoryState.ResetNoData(ctx, key) })
}

func (s *FileState) ReclaimRunning(ctx context.Context, startedBefore time.Time) (int64, error) {
	_, count, err := s.run(func() (bool, int64, error) {
		n, err := s.MemoryState.ReclaimRunning(ctx, startedBefore)
		return n > 0, n, err
	})
	return count, err
}

func (s *FileState) Close() error { return nil }

var _ model.CollectionState = (*MemoryState)(nil)
var _ model.CollectionState = (*FileState)(nil)

func PathAllowed(path string) bool {
	return path != "" && !strings.ContainsRune(path, '\x00')
}
