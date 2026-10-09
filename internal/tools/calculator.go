package tools

import (
	"context"
	"math"
)

func calculate(ctx context.Context, raw string) Result {
	if ctx.Err() != nil {
		return Result{Code: "cancelled"}
	}
	var args struct {
		Operation string   `json:"operation"`
		A         *float64 `json:"a"`
		B         *float64 `json:"b"`
	}
	if decodeArguments(raw, &args) != nil || args.A == nil || args.B == nil {
		return Result{Code: "invalid_arguments"}
	}
	a, b := *args.A, *args.B
	if math.IsInf(a, 0) || math.IsNaN(a) || math.IsInf(b, 0) || math.IsNaN(b) {
		return Result{Code: "invalid_arguments"}
	}
	var value float64
	switch args.Operation {
	case "add":
		value = a + b
	case "subtract":
		value = a - b
	case "multiply":
		value = a * b
	case "divide":
		if b == 0 {
			return Result{Code: "division_by_zero"}
		}
		value = a / b
	default:
		return Result{Code: "invalid_arguments"}
	}
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return Result{Code: "overflow"}
	}
	return Result{OK: true, Value: value}
}
