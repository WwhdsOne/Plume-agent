// Package tools 将许可声明绑定到固定执行入口，提供工作区文件工具、宿主 Shell 与可选内置工具。
// 模型返回的名字不能扩大许可范围；文件工具受 os.Root 约束，Shell 拥有当前用户权限而非沙箱权限。
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"plume-agent/internal/model"
)

// SchemaVersion 标识本包工具声明的契约版本，供请求与 trace 关联。
const SchemaVersion = "tools-v1"

// Result 是可回传给模型的结构化结果；Code 是固定枚举，不含原始参数。
// Value 可含正文或已发生副作用的证据；失败和取消也可能保留它，不能据 OK=false 推断操作已回滚。
// Summary 仅供 TUI/trace 使用，必须是固定格式的数量或退出码摘要，不得包含文件、命令正文或凭据。
type Result struct {
	OK        bool   `json:"ok"`
	Value     any    `json:"result,omitempty"`
	Code      string `json:"error,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Summary   string `json:"-"`
}

// JSON 序列化模型可见字段；Summary 不参与序列化，非法结果降级为固定错误。
func (r Result) JSON() string {
	b, err := json.Marshal(r)
	if err != nil {
		return `{"ok":false,"error":"invalid_result"}`
	}
	return string(b)
}

// Definition 将结构化声明与执行回调绑定；Parameters 是 JSON schema，回调负责参数语义校验与 context。
// 注册表构造后不接受动态注册；bash 等副作用能力只能通过已明确许可的回调提供。
type Definition struct {
	Name, Description, Parameters string
	Execute                       func(context.Context, string) Result
}

// Registry 保存构造时固定的许可名单与排序声明；工具自身的执行状态由对应实现管理。
type Registry struct {
	definitions  map[string]Definition
	declarations []model.ToolDeclaration
	workspace    *workspace
}

// New 拒绝重复名称、空入口及非法 schema JSON；参数的必填项与具体类型仍由工具校验。
func New(definitions ...Definition) (*Registry, error) {
	r := &Registry{definitions: make(map[string]Definition)}
	for _, d := range definitions {
		if _, ok := r.definitions[d.Name]; ok {
			return nil, errors.New("duplicate tool name")
		}
		if d.Name == "" || d.Execute == nil || !json.Valid([]byte(d.Parameters)) {
			return nil, errors.New("invalid tool definition")
		}
		r.definitions[d.Name] = d
		r.declarations = append(r.declarations, model.ToolDeclaration{Name: d.Name, Description: d.Description, Parameters: d.Parameters})
	}
	sort.Slice(r.declarations, func(i, j int) bool { return r.declarations[i].Name < r.declarations[j].Name })
	return r, nil
}

// Declarations 返回按名称排序的副本，调用者修改切片不会改变实际许可声明。
func (r *Registry) Declarations() []model.ToolDeclaration {
	if r == nil {
		return nil
	}
	return append([]model.ToolDeclaration(nil), r.declarations...)
}

// Known 检查名称是否在许可名单中；nil 注册表等同于没有许可。
func (r *Registry) Known(name string) bool {
	if r == nil {
		return false
	}
	_, ok := r.definitions[name]
	return ok
}

// Execute 只调用已许可入口，并在执行前后统一检查取消与期限。
// 取消后保留已有 Value/Truncated/Summary，以展示部分输出或副作用；此处不回滚、不重试也不调度并行。
func (r *Registry) Execute(ctx context.Context, name, args string) Result {
	if err := ctx.Err(); err != nil {
		return Result{Code: contextErrorCode(err)}
	}
	if !r.Known(name) {
		return Result{Code: "unknown_tool"}
	}
	result := r.definitions[name].Execute(ctx, args)
	if err := ctx.Err(); err != nil {
		result.OK = false
		result.Code = contextErrorCode(err)
	}
	return result
}

func contextErrorCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "cancelled"
}

// decodeArguments 先检查精确键名、重复键、null 与尾随数据，再解码具体类型。
// 两次解析避免 encoding/json 接受重复键或大小写别名；具体工具用指针字段区分必填值缺失与零值。
func decodeArguments(raw string, out any) error {
	if !utf8.ValidString(raw) {
		return errors.New("invalid argument encoding")
	}
	typeOf := reflect.TypeOf(out)
	if typeOf == nil || typeOf.Kind() != reflect.Pointer || typeOf.Elem().Kind() != reflect.Struct {
		return errors.New("invalid argument target")
	}
	typeOf = typeOf.Elem()
	allowed := make(map[string]bool, typeOf.NumField())
	for i := 0; i < typeOf.NumField(); i++ {
		field := typeOf.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		allowed[name] = true
	}
	d := json.NewDecoder(strings.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("expected argument object")
	}
	keys := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		if !ok || keys[name] {
			return errors.New("duplicate argument")
		}
		// encoding/json 会接受大小写别名；schema 只允许精确的 JSON 标签。
		if !allowed[name] {
			return errors.New("unknown argument")
		}
		keys[name] = true
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return err
		}
		if strings.TrimSpace(string(value)) == "null" {
			return errors.New("null argument")
		}
	}
	if _, err = d.Token(); err != nil {
		return err
	}
	if _, err = d.Token(); err != io.EOF {
		return errors.New("trailing argument data")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("invalid arguments")
	}
	return nil
}
