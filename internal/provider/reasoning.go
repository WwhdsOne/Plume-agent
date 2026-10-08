package provider

import (
	"context"
	"net/url"
	"strings"

	"plume-agent/internal/model"
)

// ReasoningPolicy 区分请求偏好与经端点、协议和模型共同验证的有效值。
type ReasoningPolicy struct {
	Requested  model.ReasoningEffort
	Effective  model.ReasoningEffort
	Source     string
	Capability string
}

func ResolveReasoning(spec ModelSpec) (ReasoningPolicy, error) {
	p := ReasoningPolicy{Requested: model.ReasoningHigh, Source: "default", Capability: "provider_default"}
	if spec.ReasoningEffort != nil {
		p.Requested = model.ReasoningEffort(*spec.ReasoningEffort)
		p.Source = "configured"
		switch p.Requested {
		case model.ReasoningNone, model.ReasoningLow, model.ReasoningMedium, model.ReasoningHigh, model.ReasoningMax:
		default:
			return p, invalidConfig("invalid reasoning_effort")
		}
	}
	u, err := url.Parse(spec.BaseURL)
	verified := err == nil && u.Scheme == "https" && u.Host == "api.deepseek.com" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (strings.TrimRight(u.Path, "/") == "" || strings.TrimRight(u.Path, "/") == "/v1") && spec.Provider == "deepseek" && spec.Protocol == ProtocolDeepSeek && (spec.Model == "deepseek-flash" || spec.Model == "deepseek-v4-pro")
	if !verified {
		if spec.ReasoningEffort != nil {
			return p, model.NewError(model.ErrUnsupported).WithSummary("reasoning_effort is not verified for this endpoint and model")
		}
		return p, nil
	}
	p.Capability = "verified"
	p.Effective = p.Requested
	if p.Effective == model.ReasoningMedium {
		p.Effective = model.ReasoningHigh
	}
	return p, nil
}

type reasoningClient struct {
	model.Client
	effort model.ReasoningEffort
}

func (c reasoningClient) Generate(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	req.ReasoningEffort = c.effort
	return c.Client.Generate(ctx, req)
}

func (c reasoningClient) Stream(ctx context.Context, req model.ChatRequest) (model.EventStream, error) {
	req.ReasoningEffort = c.effort
	return c.Client.Stream(ctx, req)
}
