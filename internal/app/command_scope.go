package app

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// LocalScopeKey 是本地权限域的固定键；工作区由存储操作独立传递。
const LocalScopeKey = "local"

// Scope 只接受入口鉴权后的身份；解析器不从正文、标题或 session ID 构造它。
// gateway 适配器必须自行验证传输来源并拒绝本地身份，不能信任客户端正文自称身份。
type Scope struct {
	Channel         string
	AccountInstance string
	Peer            string
	Thread          string
	Sender          string
}

// LocalScope 显式构造本地身份，零值也只在本地入口解释为本地身份。
func LocalScope() Scope { return Scope{Channel: LocalScopeKey} }

// IsLocal 报告是否为合法本地形状，携带任何远端字段的 local 声明不会匹配。
func (s Scope) IsLocal() bool {
	return (s.Channel == "" || s.Channel == LocalScopeKey) && s.AccountInstance == "" && s.Peer == "" && s.Thread == "" && s.Sender == ""
}

// Validate 拒绝缺失 gateway 身份和会在键编码时替换的非法 UTF-8。
func (s Scope) Validate() error {
	if s.IsLocal() {
		return nil
	}
	if s.Channel == LocalScopeKey {
		return errors.New("local scope must not contain channel identity")
	}
	for _, field := range [...]string{s.Channel, s.AccountInstance, s.Peer, s.Thread, s.Sender} {
		if strings.TrimSpace(field) == "" || !utf8.ValidString(field) {
			return errors.New("trusted channel identity is incomplete or invalid")
		}
	}
	return nil
}

// Key 生成无字段分隔符碰撞的权限键，不合格身份不会产生可查询的键。
func (s Scope) Key() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	if s.IsLocal() {
		return LocalScopeKey, nil
	}
	// 定长 JSON 元组保留字段边界；不用分隔符拼接，以免平台 ID 自带分隔符撞键。
	encoded, err := json.Marshal([...]string{s.Channel, s.AccountInstance, s.Peer, s.Thread, s.Sender})
	if err != nil {
		return "", err
	}
	return "gateway:" + string(encoded), nil
}
