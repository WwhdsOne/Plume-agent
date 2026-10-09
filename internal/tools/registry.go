// Package tools 提供许可工具声明与只读执行。模型返回的名字不能扩展许可范围。
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

const SchemaVersion = "tools-v1"

// Result 是可回传给模型的结构化结果；Code 是固定枚举，不含原始参数。
type Result struct {
	OK        bool   `json:"ok"`
	Value     any    `json:"result,omitempty"`
	Code      string `json:"error,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Summary   string `json:"-"`
}

func (r Result) JSON() string {
	b, err := json.Marshal(r)
	if err != nil {
		return `{"ok":false,"error":"invalid_result"}`
	}
	return string(b)
}

// Definition 将声明与执行绑定，启动后只读，不提供动态注册或脚本执行。
type Definition struct {
	Name, Description, Parameters string
	Execute                       func(context.Context, string) Result
}
type Registry struct {
	definitions  map[string]Definition
	declarations []model.ToolDeclaration
	workspace    *workspace
}

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
func (r *Registry) Declarations() []model.ToolDeclaration {
	if r == nil {
		return nil
	}
	return append([]model.ToolDeclaration(nil), r.declarations...)
}
func (r *Registry) Known(name string) bool {
	if r == nil {
		return false
	}
	_, ok := r.definitions[name]
	return ok
}
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

// decodeArguments 校验顶层对象、重名/未知字段及尾随数据；具体工具校验类型与必填项。
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
