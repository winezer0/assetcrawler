//go:build !routeexec

package routegraph

import (
	"errors"
	"time"
)

// ErrExecUnavailable 表示当前构建没有编入执行兜底。
//
// 执行兜底会链接 goja 的完整 VM，实测让二进制从 15.2MB 涨到 23.7MB（+8.5MB）。
// 它只覆盖「路由表由运行时 map 生成」这一种情况，所以做成编译期可选：
//
//	go build -tags routeexec ./cmd/assetcrawler
var ErrExecUnavailable = errors.New("执行兜底未编入本二进制：请用 go build -tags routeexec 重新编译")

// ExecOptions 与启用标签时保持一致，便于调用方代码无需改动。
type ExecOptions struct {
	// Timeout 单次执行的时间上限，默认 2 秒。
	Timeout time.Duration
	// MaxSourceBytes 超过这个体积不执行，默认 2MB。
	MaxSourceBytes int
}

// ExtractByExec 在未启用 routeexec 标签时直接返回 ErrExecUnavailable。
func ExtractByExec(src []byte, opts ExecOptions) (Result, error) {
	_ = src
	_ = opts
	return Result{}, ErrExecUnavailable
}
