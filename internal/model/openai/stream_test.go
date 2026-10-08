package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"plume-agent/internal/model"
	"plume-agent/internal/model/endpoint"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func streamServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(s.Close)
	return s
}

const answerFrame = `data: {"id":"c","choices":[{"index":0,"delta":{"content":"你好"},"finish_reason":null}]}` + "\n\n"
const finishFrame = `data: {"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n"
const doneFrame = "data: [DONE]\n\n"

func TestStreamReadsAnswerUsageAndEnd(t *testing.T) {
	s := streamServer(t, answerFrame+finishFrame+`data: {"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`+"\n\n"+doneFrame)
	stream, err := newTestAdapter(t, s.URL).Stream(context.Background(), simpleRequest("m"))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	var text string
	var usage model.Usage
	var finished, ended int
	for stream.Next(context.Background()) {
		e := stream.Event()
		switch e.Kind {
		case model.EventTextDelta:
			text += e.TextDelta
		case model.EventUsageUpdate:
			usage = e.Usage
		case model.EventModelDone:
			finished++
		case model.EventStreamEnded:
			ended++
		}
	}
	if stream.Err() != nil {
		t.Fatal(stream.Err())
	}
	if text != "你好" || !usage.OK || usage.TotalTokens != 5 || finished != 1 || ended != 1 {
		t.Fatalf("text=%q usage=%+v finish=%d end=%d", text, usage, finished, ended)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamRequiresFinishAndDoneEvidence(t *testing.T) {
	for _, body := range []string{answerFrame, answerFrame + finishFrame, answerFrame + doneFrame} {
		s := streamServer(t, body)
		stream, err := newTestAdapter(t, s.URL).Stream(context.Background(), simpleRequest("m"))
		if err != nil {
			t.Fatal(err)
		}
		for stream.Next(context.Background()) {
		}
		assertCode(t, stream.Err(), model.ErrStreamInterrupt)
		_ = stream.Close()
	}
}

func TestUnverifiedReasoningControlRejectedWithoutHTTP(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()
	req := simpleRequest("m")
	req.ReasoningEffort = model.ReasoningHigh
	_, err := newTestAdapter(t, srv.URL).Generate(context.Background(), req)
	assertCode(t, err, model.ErrUnsupported)
	if called {
		t.Fatal("unsupported control sent HTTP")
	}
}

func TestStreamProtocolFixtures(t *testing.T) {
	cases := []struct {
		name, body string
		code       model.ErrCode
	}{
		{"malformed", "data: {broken}\n\n", model.ErrInvalidResponse},
		{"multi-choice", `data: {"choices":[{"index":0,"delta":{"content":"a"}},{"index":1,"delta":{"content":"b"}}]}` + "\n\n", model.ErrInvalidResponse},
		{"wrong-index", `data: {"choices":[{"index":2,"delta":{"content":"a"}}]}` + "\n\n", model.ErrInvalidResponse},
		{"choice-after-finish", answerFrame + finishFrame + answerFrame + doneFrame, model.ErrInvalidResponse},
		{"empty-answer", finishFrame + doneFrame, model.ErrInvalidResponse},
		{"fake-done", answerFrame + finishFrame + "data: [DONE]garbage\n\n", model.ErrInvalidResponse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := streamServer(t, tc.body)
			s, err := newTestAdapter(t, srv.URL).Stream(context.Background(), simpleRequest("m"))
			if err != nil {
				t.Fatal(err)
			}
			for s.Next(context.Background()) {
			}
			assertCode(t, s.Err(), tc.code)
			_ = s.Close()
		})
	}
}

func TestSDKHandlesCRLFMultilineAndSplitUTF8(t *testing.T) {
	body := ": keepalive\r\n\r\ndata: {\r\ndata: \"choices\":[{\"index\":0,\"delta\":{\"content\":\"中文😀\"}}]}\r\n\r\n" + strings.ReplaceAll(finishFrame+doneFrame, "\n", "\r\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, b := range []byte(body) {
			_, _ = w.Write([]byte{b})
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()
	s, err := newTestAdapter(t, srv.URL).Stream(context.Background(), simpleRequest("m"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var answer string
	for s.Next(context.Background()) {
		answer += s.Event().TextDelta
	}
	if s.Err() != nil || answer != "中文😀" {
		t.Fatalf("answer=%q err=%v", answer, s.Err())
	}
}

func TestStreamCancelBlockedNextAndConcurrentClose(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	s, err := newTestAdapter(t, srv.URL).Stream(context.Background(), simpleRequest("m"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan struct{})
	go func() { defer close(ended); s.Next(ctx) }()
	cancel()
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("blocked Next did not cancel")
	}
	assertCode(t, s.Err(), model.ErrCancelled)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = newTestAdapter(t, srv.URL).Stream(context.Background(), simpleRequest("m"))
	if err != nil {
		t.Fatal(err)
	}
	ended = make(chan struct{})
	go func() { defer close(ended); s.Next(context.Background()) }()
	_ = s.Close()
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("Close did not interrupt Next")
	}
}

func TestStreamReadsLimitedBytesAndKeepsUnknownUsage(t *testing.T) {
	srv := streamServer(t, answerFrame+finishFrame+doneFrame)
	s, err := newTestAdapter(t, srv.URL).Stream(context.Background(), simpleRequest("m"))
	if err != nil {
		t.Fatal(err)
	}
	for s.Next(context.Background()) {
		if s.Event().Kind == model.EventUsageUpdate {
			t.Fatal("missing usage fabricated")
		}
	}
	if s.Err() != nil {
		t.Fatal(s.Err())
	}
	_ = s.Close()
	// 注释帧也占协议字节，不能让无内容流绕过总读取上限。
	srv = streamServer(t, strings.Repeat(": "+strings.Repeat("x", 1024)+"\n\n", 17000))
	s, err = newTestAdapter(t, srv.URL).Stream(context.Background(), simpleRequest("m"))
	if err != nil {
		t.Fatal(err)
	}
	for s.Next(context.Background()) {
	}
	assertCode(t, s.Err(), model.ErrResponseTooLarge)
	_ = s.Close()
}

func TestStreamToolIndicesRemainStable(t *testing.T) {
	first := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call-x","function":{"name":"clock","arguments":"{"}}]}}]}` + "\n\n"
	second := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n"
	srv := streamServer(t, first+second+doneFrame)
	s, err := newTestAdapter(t, srv.URL).Stream(context.Background(), simpleRequest("m"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var args string
	calls := 0
	for s.Next(context.Background()) {
		e := s.Event()
		if e.Kind == model.EventToolDelta {
			if e.ToolIndex != 1 || e.ToolCall.ID != "call-x" {
				t.Fatalf("event=%+v", e)
			}
			args += e.ToolCall.Arguments
			calls++
		}
	}
	if s.Err() != nil || args != "{}" || calls != 2 {
		t.Fatalf("args=%q calls=%d err=%v", args, calls, s.Err())
	}
	srv = streamServer(t, first+strings.Replace(second, `"index":1,"function"`, `"index":1,"id":"changed","function"`, 1)+doneFrame)
	s, err = newTestAdapter(t, srv.URL).Stream(context.Background(), simpleRequest("m"))
	if err != nil {
		t.Fatal(err)
	}
	for s.Next(context.Background()) {
	}
	assertCode(t, s.Err(), model.ErrInvalidResponse)
	_ = s.Close()
}

func TestStreamHTTPFailureDoesNotRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "rate limited"}})
	}))
	defer srv.Close()
	_, err := newTestAdapter(t, srv.URL).Stream(context.Background(), simpleRequest("m"))
	assertCode(t, err, model.ErrRateLimited)
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = newTestAdapter(t, srv.URL).Stream(ctx, simpleRequest("m"))
	assertCode(t, err, model.ErrCancelled)
	if calls.Load() != 1 {
		t.Fatal("cancelled request sent HTTP")
	}
}

func TestStreamBrokenReadPreservesInterruptionClassification(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: %d\r\n\r\n%s", len(answerFrame)+100, answerFrame)
		_ = buf.Flush()
	}))
	defer srv.Close()
	s, err := newTestAdapter(t, srv.URL).Stream(context.Background(), simpleRequest("m"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.Next(context.Background()) || s.Event().TextDelta != "你好" {
		t.Fatal("partial answer lost")
	}
	for s.Next(context.Background()) {
	}
	assertCode(t, s.Err(), model.ErrStreamInterrupt)
}

func TestStreamDeclaredSizeUsesSixteenMiBLimit(t *testing.T) {
	filler := strings.Repeat(": "+strings.Repeat("x", 1024)+"\n\n", 9000)
	body := filler + answerFrame + finishFrame + doneFrame
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	s, err := newTestAdapter(t, srv.URL).Stream(context.Background(), simpleRequest("m"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for s.Next(context.Background()) {
	}
	if s.Err() != nil {
		t.Fatal(s.Err())
	}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", fmt.Sprint(endpoint.MaxStreamBytes+1))
		w.WriteHeader(200)
	}))
	defer srv.Close()
	_, err = newTestAdapter(t, srv.URL).Stream(context.Background(), simpleRequest("m"))
	assertCode(t, err, model.ErrResponseTooLarge)
}

func TestAdapterCapabilityProfilesDoNotClaimUnverifiedControl(t *testing.T) {
	generic, err := NewAdapter("deepseek", "https://example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	deepseek, err := NewDeepSeekAdapter("https://example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Adapter{generic, deepseek} {
		caps := a.Capabilities()
		if caps.State(model.CapStream) != model.CapSupported || caps.State(model.CapReasoningEffort) != model.CapUnverified || caps.State(model.CapReasoningDisable) != model.CapUnverified {
			t.Fatal("incorrect capability profile")
		}
	}
	if generic.Capabilities().State(model.CapReasoningOutput) != model.CapUnverified || deepseek.Capabilities().State(model.CapReasoningOutput) != model.CapSupported {
		t.Fatal("brand must not select protocol capabilities")
	}
}
