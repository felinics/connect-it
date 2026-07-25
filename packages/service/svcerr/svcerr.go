// Package svcerr 是 service 层对外的错误词汇表。transport 层只认识这里的
// 有限 Kind，不必逐个 errors.Is 各业务包的 sentinel；反过来，业务包新增
// sentinel 也不会悄悄退化成 500。
package svcerr

import "errors"

// Kind 是 service 层暴露给 transport 层的封闭失败分类。每个 Kind 对应一种
// 对外可观测的结果，新增 Kind 必须同时在 api 的映射表里给出状态码。
type Kind int

const (
	// NotFound：资源不存在，或路径 ID 形态非法（两者对调用方不可区分）。
	NotFound Kind = iota + 1
	// ConfigConflict：Connector 配置的乐观并发冲突。
	ConfigConflict
	// ConnectionConflict：Connection / authorization 的乐观并发冲突。
	ConnectionConflict
	// ConfigIncompatible：库中配置版本比当前代码新，拒绝覆盖写。
	ConfigIncompatible
	// Invalid：请求参数不合法。
	Invalid
	// EgressRejected：出站策略拒绝；细节只进日志，响应用固定文案。
	EgressRejected
	// VerifyFailed：Remote MCP 实测失败。
	VerifyFailed
)

// Error 是带分类的 service 层错误。Msg 会被 Invalid / VerifyFailed 原样回给
// 调用方，因此不得包含 Provider 原始响应或 credential 值。
type Error struct {
	Kind Kind
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// New 声明一个带分类的错误；业务包用它定义 sentinel，errors.Is 依旧按指针相等
// 匹配，From 则让 transport 层拿到 Kind。
func New(kind Kind, msg string) *Error { return &Error{Kind: kind, Msg: msg} }

// Classifier 让自带字段的错误类型（如 configsvc.ValidationError）加入词汇表，
// 同时保留自己的消息形状。
type Classifier interface{ ServiceError() *Error }

// From 把任意错误归入词汇表；不属于词汇表的返回 nil，由调用方按内部故障处理。
func From(err error) *Error {
	var classified *Error
	if errors.As(err, &classified) {
		return classified
	}
	var classifier Classifier
	if errors.As(err, &classifier) {
		return classifier.ServiceError()
	}
	return nil
}
