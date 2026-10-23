package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"plume-agent/internal/model"
	"plume-agent/internal/store"
)

// cancelAcknowledgementLoss 模拟收据已提交但确认丢失、第一次核查也暂不可用。
type cancelAcknowledgementLoss struct {
	store.Store
	store.ManagementStore
	queries int
}

func (f *cancelAcknowledgementLoss) GetReceipt(ctx context.Context, scope store.Scope, id string) (store.Receipt, bool, error) {
	f.queries++
	if f.queries == 2 {
		return store.Receipt{}, false, errors.New("lookup temporarily unavailable")
	}
	return f.ManagementStore.GetReceipt(ctx, scope, id)
}

func (f *cancelAcknowledgementLoss) ApplyCommand(ctx context.Context, mutation store.CommandMutation) (store.Receipt, bool, error) {
	if _, _, err := f.ManagementStore.ApplyCommand(ctx, mutation); err != nil {
		return store.Receipt{}, false, err
	}
	return store.Receipt{}, false, errors.New("commit acknowledgement lost")
}

// 独立审核发现：无法确认已存取消收据时，重投须补发原 run 的取消信号。
func TestCommandUncertainCancelReplayStillCancelsFixedRun(t *testing.T) {
	base := store.NewMemory()
	backend := &cancelAcknowledgementLoss{Store: base, ManagementStore: base}
	wait := make(chan struct{})
	client := model.NewFake(model.FakeScript{Stream: []model.FakeStep{{Wait: wait, Event: model.Event{Kind: model.EventTextDelta, TextDelta: "late"}}}})
	service := commandService(t, backend, gatewayScope(), client)
	if result := service.HandleInput(context.Background(), CommandRequest{Text: "hello", Scope: gatewayScope(), Entrance: EntranceGateway}); result.Code != "ok" {
		t.Fatal(result)
	}
	request := CommandRequest{Text: "/cancel", Scope: gatewayScope(), Entrance: EntranceGateway, RequestID: "cancel"}
	if first := service.HandleInput(context.Background(), request); first.Code != "history_uncertain" {
		t.Fatal(first)
	}
	if replay := service.HandleInput(context.Background(), request); replay.Code != "cancel_requested" || !replay.Replayed {
		t.Fatal(replay)
	}
	deadline := time.After(time.Second)
	for {
		select {
		case event := <-service.Events():
			if event.Kind == EventRunFailed || event.Kind == EventRunCompleted {
				return
			}
		case <-deadline:
			t.Fatal("stored cancel receipt did not cancel its fixed target")
		}
	}
}
