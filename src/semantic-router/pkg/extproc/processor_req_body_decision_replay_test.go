package extproc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"testing"
	"time"

	core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	ext_proc "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/config"
	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/headers"
	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/routerreplay"
	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/routerreplay/store"
)

func newDecisionOnlyReplayTestRouter(t *testing.T) (*OpenAIRouter, *routerreplay.Recorder) {
	t.Helper()
	router := newDecisionOnlyTestRouter(t)
	router.Config.VLLMEndpoints = nil
	router.Config.RouterReplay = config.RouterReplayConfig{Enabled: true, StoreBackend: "memory"}
	recorder := routerreplay.NewRecorder(store.NewMemoryStore(10, 0))
	router.ReplayRecorder = recorder
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	return router, recorder
}

func TestDecisionOnlyReplayRequestFlow(t *testing.T) {
	for _, tc := range []struct {
		name, model, content, wantModel, wantMethod, wantDecision string
		stream                                                    bool
	}{
		{name: "rule", model: "auto", content: "coding", wantModel: "Pro", wantMethod: "routing", wantDecision: "code"},
		{name: "stream request", model: "auto", content: "coding", stream: true, wantModel: "Pro", wantMethod: "routing", wantDecision: "code"},
		{name: "default fallback", model: "auto", content: "hello", wantModel: "Fast", wantMethod: "fallback"},
		{name: "explicit type", model: "gateway-pro", content: "hello", wantModel: "gateway-pro", wantMethod: "passthrough"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router, recorder := newDecisionOnlyReplayTestRouter(t)
			ctx := prepareDecisionOnlyFlow(t, router)
			ctx.RequestID = "decision-only-replay-test"
			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q}],"stream":%v}`, tc.model, tc.content, tc.stream)
			response, err := router.handleRequestBody(decisionOnlyBodyRequest(body), ctx)
			require.NoError(t, err)
			immediate := response.GetImmediateResponse()
			require.NotNil(t, immediate)
			require.EqualValues(t, 200, immediate.GetStatus().GetCode())
			require.Nil(t, response.GetRequestBody())
			require.Zero(t, ctx.InflightToken)
			var payload selectedModelDecisionPayload
			require.NoError(t, json.Unmarshal(immediate.GetBody(), &payload))
			require.Equal(t, tc.wantModel, payload.SelectedModel)
			require.Equal(t, tc.wantMethod, payload.SelectionMethod)
			require.NotNil(t, payload.Debug)
			require.Len(t, recorder.ListAllRecords(), 1)
			record, found := recorder.GetRecord(ctx.RouterReplayID)
			require.True(t, found)
			require.Equal(t, ctx.RouterReplayID, headerValuesByName(immediate.GetHeaders().GetSetHeaders())[headers.RouterReplayID])
			require.Equal(t, ctx.RequestID, record.RequestID)
			require.Equal(t, tc.model, record.OriginalModel)
			require.Equal(t, tc.wantModel, record.SelectedModel)
			require.Equal(t, tc.wantDecision, record.Decision)
			require.Equal(t, ctx.VSRContextTokenCount, payload.Debug.Context.ContextTokens)
			require.Equal(t, ctx.VSRMatchedKeywords, record.Signals.Keyword)
			require.Equal(t, body, record.RequestBody)
			require.Equal(t, string(immediate.GetBody()), record.ResponseBody)
			requireDecisionOnlyReplayUsage(t, record)
		})
	}
}

func requireDecisionOnlyReplayUsage(t *testing.T, record routerreplay.RoutingRecord) {
	t.Helper()
	require.Equal(t, 200, record.ResponseStatus)
	require.False(t, record.Streaming)
	require.False(t, record.FromCache)
	for _, tokens := range []*int{record.PromptTokens, record.CachedPromptTokens, record.CompletionTokens, record.TotalTokens} {
		require.NotNil(t, tokens)
		require.Zero(t, *tokens)
	}
	for _, cost := range []*float64{record.ActualCost, record.BaselineCost, record.CostSavings} {
		require.NotNil(t, cost)
		require.Zero(t, *cost)
	}
}

func TestDecisionOnlyReplayCapturePolicy(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		globalDisabled, noRecorder, noRecord bool
		plugin                               map[string]interface{}
		noBodies, truncated                  bool
	}{
		{name: "disabled", globalDisabled: true, noRecord: true},
		{name: "no recorder", noRecorder: true, noRecord: true},
		{name: "decision opt out", plugin: map[string]interface{}{"enabled": false}, noRecord: true},
		{name: "decision opt in", globalDisabled: true, plugin: map[string]interface{}{"enabled": true}},
		{name: "body capture disabled", plugin: map[string]interface{}{"capture_request_body": false, "capture_response_body": false}, noBodies: true},
		{name: "body truncation", plugin: map[string]interface{}{"max_body_bytes": 16}, truncated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router, recorder := newDecisionOnlyReplayTestRouter(t)
			router.Config.RouterReplay.Enabled = !tc.globalDisabled
			if tc.noRecorder {
				router.ReplayRecorder = nil
			}
			if tc.plugin != nil {
				router.Config.Decisions[0].Plugins = []config.DecisionPlugin{{
					Type: config.DecisionPluginRouterReplay, Configuration: config.MustStructuredPayload(tc.plugin),
				}}
			}
			ctx := prepareDecisionOnlyFlow(t, router)
			body := `{"model":"auto","messages":[{"role":"user","content":"coding"}]}`
			response, err := router.handleRequestBody(decisionOnlyBodyRequest(body), ctx)
			require.NoError(t, err)
			immediate := response.GetImmediateResponse()
			require.NotNil(t, immediate)
			require.EqualValues(t, 200, immediate.GetStatus().GetCode())
			if tc.noRecord {
				require.Empty(t, ctx.RouterReplayID)
				require.NotContains(t, headerValuesByName(immediate.GetHeaders().GetSetHeaders()), headers.RouterReplayID)
				require.Empty(t, recorder.ListAllRecords())
				return
			}
			record, found := recorder.GetRecord(ctx.RouterReplayID)
			require.True(t, found)
			if tc.noBodies {
				require.Empty(t, record.RequestBody)
				require.Empty(t, record.ResponseBody)
			} else if tc.truncated {
				require.Equal(t, body[:16], record.RequestBody)
				require.Equal(t, string(immediate.GetBody()[:16]), record.ResponseBody)
				require.True(t, record.RequestBodyTruncated)
				require.True(t, record.ResponseBodyTruncated)
			} else {
				require.Equal(t, body, record.RequestBody)
				require.Equal(t, string(immediate.GetBody()), record.ResponseBody)
			}
			requireDecisionOnlyReplayUsage(t, record)
		})
	}
}

type decisionOnlyFailingReplayStore struct {
	store.Storage
	operation string
}

func (s *decisionOnlyFailingReplayStore) Add(ctx context.Context, record store.Record) (string, error) {
	if s.operation == "add" {
		return "", errors.New("replay add failed")
	}
	return s.Storage.Add(ctx, record)
}

func (s *decisionOnlyFailingReplayStore) UpdateStatus(ctx context.Context, id string, status int, fromCache bool, streaming bool) error {
	if s.operation == "status" {
		return errors.New("replay status failed")
	}
	return s.Storage.UpdateStatus(ctx, id, status, fromCache, streaming)
}

func (s *decisionOnlyFailingReplayStore) UpdateUsageCost(ctx context.Context, id string, usage store.UsageCost) error {
	if s.operation == "usage" {
		return errors.New("replay usage failed")
	}
	return s.Storage.UpdateUsageCost(ctx, id, usage)
}

func (s *decisionOnlyFailingReplayStore) AttachResponse(ctx context.Context, id string, body string, truncated bool) error {
	if s.operation == "response" {
		return errors.New("replay response failed")
	}
	return s.Storage.AttachResponse(ctx, id, body, truncated)
}

func TestDecisionOnlyReplayStorageFailures(t *testing.T) {
	for _, operation := range []string{"add", "status", "usage", "response"} {
		t.Run(operation, func(t *testing.T) {
			router, _ := newDecisionOnlyReplayTestRouter(t)
			recorder := routerreplay.NewRecorder(&decisionOnlyFailingReplayStore{Storage: store.NewMemoryStore(10, 0), operation: operation})
			router.ReplayRecorder = recorder
			t.Cleanup(func() { require.NoError(t, recorder.Close()) })
			ctx := prepareDecisionOnlyFlow(t, router)
			response, err := router.handleRequestBody(decisionOnlyBodyRequest(`{"model":"auto","messages":[{"role":"user","content":"coding"}]}`), ctx)
			require.NoError(t, err)
			immediate := response.GetImmediateResponse()
			require.NotNil(t, immediate)
			require.EqualValues(t, 200, immediate.GetStatus().GetCode())
			var payload selectedModelDecisionPayload
			require.NoError(t, json.Unmarshal(immediate.GetBody(), &payload))
			require.Equal(t, "Pro", payload.SelectedModel)
			require.NotNil(t, payload.Debug)
			if operation == "add" {
				require.Empty(t, ctx.RouterReplayID)
				require.NotContains(t, headerValuesByName(immediate.GetHeaders().GetSetHeaders()), headers.RouterReplayID)
			} else {
				require.NotEmpty(t, ctx.RouterReplayID)
			}
		})
	}
}

func TestDecisionOnlyReplayErrorResponses(t *testing.T) {
	for _, tc := range []struct {
		name       string
		noDefault  bool
		confidence float64
		wantStatus int
	}{
		{name: "no selected or default type", noDefault: true, wantStatus: 400},
		{name: "response serialization failed", confidence: math.Inf(1), wantStatus: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router, recorder := newDecisionOnlyReplayTestRouter(t)
			ctx := &RequestContext{VSRSelectedDecisionConfidence: tc.confidence, RouterReplayPluginConfig: router.Config.EffectiveRouterReplayConfigForDecision("code")}
			state := requestDecisionState{decisionName: "code", selectedModel: "Pro"}
			if tc.noDefault {
				router.Config.DefaultModel = ""
				state = requestDecisionState{}
			}
			response := router.buildDecisionOnlySelectedModelResponse("auto", state, ctx)
			require.EqualValues(t, tc.wantStatus, response.GetImmediateResponse().GetStatus().GetCode())
			require.Empty(t, ctx.RouterReplayID)
			require.Empty(t, recorder.ListAllRecords())
		})
	}
}

func TestDecisionOnlyReplayGRPCRequestStream(t *testing.T) {
	router, recorder := newDecisionOnlyReplayTestRouter(t)
	prepareDecisionOnlyFlow(t, router)
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	ext_proc.RegisterExternalProcessorServer(server, router)
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	go func() { _ = server.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///decision-only-replay-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := ext_proc.NewExternalProcessorClient(conn).Process(ctx)
	require.NoError(t, err)
	require.NoError(t, stream.Send(&ext_proc.ProcessingRequest{Request: &ext_proc.ProcessingRequest_RequestHeaders{
		RequestHeaders: &ext_proc.HttpHeaders{Headers: &core.HeaderMap{Headers: []*core.HeaderValue{
			{Key: ":method", Value: "POST"}, {Key: ":path", Value: "/v1/chat/completions"},
			{Key: "content-type", Value: "application/json"}, {Key: "x-request-id", Value: "replay-grpc-test"},
		}}},
	}}))
	response, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, response.GetRequestHeaders())
	body := `{"model":"auto","messages":[{"role":"user","content":"coding"}],"stream":true}`
	require.NoError(t, stream.Send(&ext_proc.ProcessingRequest{Request: decisionOnlyBodyRequest(body)}))
	response, err = stream.Recv()
	require.NoError(t, err)
	immediate := response.GetImmediateResponse()
	require.NotNil(t, immediate)
	require.EqualValues(t, 200, immediate.GetStatus().GetCode())
	replayID := headerValuesByName(immediate.GetHeaders().GetSetHeaders())[headers.RouterReplayID]
	require.NotEmpty(t, replayID)
	record, found := recorder.GetRecord(replayID)
	require.True(t, found)
	require.Equal(t, "replay-grpc-test", record.RequestID)
	require.Equal(t, "Pro", record.SelectedModel)
	require.Equal(t, "code", record.Decision)
	require.Equal(t, body, record.RequestBody)
	require.Equal(t, string(immediate.GetBody()), record.ResponseBody)
	requireDecisionOnlyReplayUsage(t, record)
	require.NoError(t, stream.CloseSend())
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)
}
