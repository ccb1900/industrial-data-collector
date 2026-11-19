package errs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// 错误分类：采集主流程不解析原始 OS/数据库报错文本，组件把底层错误映射到这些
// 类别。类名会被拼进每条对外报错（"类名: 明细"），所以用中文；机器侧判定走
// errs.Is(ErrXxx) 与 hub 的 code 字段，不依赖这里的文本。
var (
	ErrNotFound         = errors.New("未找到")
	ErrPermissionDenied = errors.New("权限不足")
	ErrUnavailable      = errors.New("资源不可用")
	ErrTimeout          = errors.New("操作超时")
	ErrIO               = errors.New("读写错误")
	ErrSourceChanged    = errors.New("读取期间源文件已变化")
	// ErrNoNewContent 是 flat 源（整文件 + 内容哈希去重）的"文件在、内容没变"：
	// 数据存在且已采过。它不是 NotFound——把去重后的空清单当"没有数据"计数，
	// 会在 append 模式下把正在生产的机台终态化成"确认无数据"。
	ErrNoNewContent    = errors.New("无新内容")
	ErrFileUnstable    = errors.New("文件尚未稳定")
	ErrInvalidEncoding = errors.New("CSV 编码无效")
	ErrMalformedCSV    = errors.New("CSV 格式无效")
	ErrInvalidFile     = errors.New("文件标识无效")
	ErrInvalidConfig   = errors.New("应用配置无效")
	ErrConfiguration   = errors.New("配置错误")
	// ErrDateBoundary 是业务日越界：今天/未来日期进入自动采集，或自动目标早于
	// 补采窗口首日。它既不是"未找到"也不是配置无效——报出去要让现场一眼看出是
	// 日期口径问题，而不是设备或文件出了故障。
	ErrDateBoundary       = errors.New("业务日期越界")
	ErrDependency         = errors.New("依赖不可用")
	ErrStorageConnection  = errors.New("存储连接错误")
	ErrStorageTimeout     = errors.New("存储超时")
	ErrStorageAuth        = errors.New("存储认证错误")
	ErrStorageConstraint  = errors.New("存储约束冲突")
	ErrStorageTransient   = errors.New("存储临时故障")
	ErrStoragePermanent   = errors.New("存储永久故障")
	ErrMetadataExtraction = errors.New("元数据提取错误")
	ErrCancelled          = errors.New("操作已取消")
)

// SourceError attaches an application error class to an underlying error.
type SourceError struct {
	Class error
	Err   error
}

func (e *SourceError) Error() string {
	detail := ""
	if e.Err != nil {
		detail = e.Err.Error()
	}
	class := ""
	if e.Class != nil {
		class = e.Class.Error()
	}
	if class == "" || detail == "" {
		return class + detail
	}
	if strings.HasPrefix(detail, class) {
		return detail
	}
	return class + ": " + detail
}
func (e *SourceError) Unwrap() []error { return []error{e.Class, e.Err} }

func Is(err error, target error) bool { return errors.Is(err, target) }

func Sourcef(class error, format string, args ...any) error {
	return &SourceError{Class: class, Err: fmt.Errorf(format, args...)}
}

// ClassifySourceError maps an OS/path error into a domain-stable class without
// leaking vendor-specific strings into Collector.
func ClassifySourceError(path string, err error) error {
	if err == nil {
		return nil
	}
	// 网络类判定必须先于通用 NotExist：Windows 的 syscall.Errno.Is 把
	// ERROR_BAD_NETPATH（UNC 路径不可达）归入 fs.ErrNotExist——顺序颠倒
	// 会把掉线当成"路径不存在"，过期日被终态化为无数据，永不补采。
	if networkUnavailable(err) {
		return Sourcef(ErrUnavailable, "路径 %q: %w", path, err)
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
		return Sourcef(ErrNotFound, "路径 %q: %w", path, err)
	}
	if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) {
		return Sourcef(ErrPermissionDenied, "路径 %q: %w", path, err)
	}
	if errors.Is(err, syscall.ENETDOWN) || errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) {
		return Sourcef(ErrUnavailable, "路径 %q: %w", path, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Sourcef(ErrTimeout, "路径 %q: %w", path, err)
	}
	return Sourcef(ErrIO, "路径 %q: %w", path, err)
}
