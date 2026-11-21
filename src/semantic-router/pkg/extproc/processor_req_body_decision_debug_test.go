package extproc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"strings"
	"testing"
	"time"

	core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	ext_proc "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/classification"
	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/config"
	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/inflight"
)

func TestDecisionOnlyDebugGRPCRequestStream(t *testing.T) {
	for _, tc := range []struct {
		name, model, content, wantModel, wantMethod           string
		stream, looper, noEndpoints, noDecisions, compression bool
	}{
		{name: "rule", model: "auto", content: "coding", wantModel: "Pro", wantMethod: "routing"},
		{name: "fallback", model: "auto", content: "hello", wantModel: "Fast", wantMethod: "fallback"},
		{name: "explicit", model: "Pro", content: "hello", wantModel: "Pro", wantMethod: "passthrough"},
		{name: "stream request still returns decision JSON", model: "auto", content: "coding", stream: true, wantModel: "Pro", wantMethod: "routing"},
		{name: "looper opt out preserved", model: "Pro", content: "hello", looper: true},
		{name: "type without backend", model: "auto", content: "coding", noEndpoints: true, wantModel: "Pro", wantMethod: "routing"},
		{name: "default type without backend", model: "auto", content: "hello", noEndpoints: true, wantModel: "Fast", wantMethod: "fallback"},
		{name: "no decisions still returns context", model: "auto", content: "coding", noDecisions: true, wantModel: "Fast", wantMethod: "fallback"},
		{name: "compression metadata survives gRPC", model: "auto", content: "coding. " + strings.Repeat("An independent sentence with several different words. ", 100), compression: true, wantModel: "Pro", wantMethod: "routing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := newDecisionOnlyTestRouter(t)
			if tc.noEndpoints {
				router.Config.VLLMEndpoints = nil
			}
			if tc.noDecisions {
				router.Config.Decisions = nil
			}
			if tc.compression {
				router.Config.PromptCompression = config.PromptCompressionConfig{Enabled: true, MaxTokens: 32, SkipSignals: []string{"keyword"}}
			}
			prepareDecisionOnlyFlow(t, router)
			listener := bufconn.Listen(1024 * 1024)
			server := grpc.NewServer()
			ext_proc.RegisterExternalProcessorServer(server, router)
			t.Cleanup(func() { server.Stop(); _ = listener.Close() })
			go func() { _ = server.Serve(listener) }()
			conn, err := grpc.NewClient("passthrough:///decision-only-test",
				grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
			)
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream, err := ext_proc.NewExternalProcessorClient(conn).Process(ctx)
			require.NoError(t, err)
			headers := []*core.HeaderValue{
				{Key: ":method", Value: "POST"}, {Key: ":path", Value: "/v1/chat/completions"},
				{Key: "content-type", Value: "application/json"},
			}
			if tc.looper {
				headers = append(headers, &core.HeaderValue{Key: "x-vsr-looper-request", Value: "true"})
			}
			require.NoError(t, stream.Send(&ext_proc.ProcessingRequest{Request: &ext_proc.ProcessingRequest_RequestHeaders{
				RequestHeaders: &ext_proc.HttpHeaders{Headers: &core.HeaderMap{Headers: headers}},
			}}))
			response, err := stream.Recv()
			require.NoError(t, err)
			require.NotNil(t, response.GetRequestHeaders())
			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q}],"stream":%v}`, tc.model, tc.content, tc.stream)
			require.NoError(t, stream.Send(&ext_proc.ProcessingRequest{Request: decisionOnlyBodyRequest(body)}))
			response, err = stream.Recv()
			require.NoError(t, err)
			if tc.looper {
				require.Nil(t, response.GetImmediateResponse())
				require.NotNil(t, response.GetRequestBody())
			} else {
				require.NotNil(t, response.GetImmediateResponse())
				require.EqualValues(t, 200, response.GetImmediateResponse().GetStatus().GetCode())
				var payload selectedModelDecisionPayload
				require.NoError(t, json.Unmarshal(response.GetImmediateResponse().GetBody(), &payload))
				require.Equal(t, tc.wantModel, payload.SelectedModel)
				require.Equal(t, tc.wantMethod, payload.SelectionMethod)
				requireDecisionOnlyDebugGRPCPayload(t, payload, tc.compression)
			}
			require.NoError(t, stream.CloseSend())
			_, err = stream.Recv()
			require.ErrorIs(t, err, io.EOF)
		})
	}
}

func requireDecisionOnlyDebugGRPCPayload(t *testing.T, payload selectedModelDecisionPayload, compression bool) {
	t.Helper()
	require.NotNil(t, payload.Debug, "debug must survive the complete extproc gRPC exchange")
	require.Equal(t, payload.SelectedModel, payload.Debug.Routing.SelectedModel)
	require.Equal(t, payload.SelectionMethod, payload.Debug.Routing.ResponseSelectionMethod)
	require.Equal(t, 1, payload.Debug.Context.UserMessageCount)
	require.NotNil(t, payload.Debug.Timings)
	require.Equal(t, compression, payload.Debug.PromptCompression.Applied)
	if compression {
		require.Less(t, payload.Debug.PromptCompression.CompressedTokens, payload.Debug.PromptCompression.OriginalTokens)
		require.Equal(t, []string{"keyword"}, payload.Debug.PromptCompression.SkippedSignals)
	}
	if payload.SelectionMethod == "routing" {
		require.Equal(t, []string{"code_keyword"}, payload.Debug.MatchedSignals.Keyword)
		require.Equal(t, []string{"coding"}, payload.Debug.MatchedSignals.KeywordTerms)
		require.Equal(t, "code", payload.Debug.Decision.Name)
	}
}

func TestDecisionOnlyDebugResponseContract(t *testing.T) {
	router := newDecisionOnlyTestRouter(t)
	ctx := &RequestContext{
		ProcessingStartTime:           time.Now().Add(-time.Second),
		VSRSelectedDecision:           &router.Config.Decisions[0],
		VSRSelectedDecisionName:       "code",
		VSRSelectedDecisionConfidence: 0.9149,
		VSRSelectionMethod:            "single",
		VSRMatchedKeywords:            []string{"z-rule", "a-rule"},
		VSRMatchedKeywordTerms:        []string{"coding"},
		VSRDecisionMatchedRules:       []string{"keyword:a-rule"},
		VSRDecisionMatchedKeywords:    []string{"coding"},
		VSRSignalConfidences:          map[string]float64{"keyword:a-rule": 0.826},
		VSRSignalValues:               map[string]float64{"keyword:a-rule": 0.824},
		VSRProjectionScores:           map[string]float64{"score": 0.876},
		VSRContextTokenCount:          42,
		VSRContextTextBytes:           168,
		VSRConversationFacts: classification.ConversationFacts{
			UserMessageCount: 2, AssistantMessageCount: 1, ToolDefinitionCount: 1,
			LastMessageRole: "user", HasDeveloperMessage: true,
		},
		VSRDecisionTimings: requestDecisionTimingInfo{RequestParseMs: 1.23456, SignalEvaluationMs: 10.98765},
		VSRSignalMetrics: &classification.SignalMetricsCollection{
			Keyword: classification.SignalMetrics{ExecutionTimeMs: 2.34567},
		},
	}
	response := router.buildDecisionOnlySelectedModelResponse("auto", requestDecisionState{decisionName: "code", selectedModel: "Pro"}, ctx)
	require.EqualValues(t, 200, response.GetImmediateResponse().GetStatus().GetCode())
	var payload selectedModelDecisionPayload
	require.NoError(t, json.Unmarshal(response.GetImmediateResponse().GetBody(), &payload))
	debug := payload.Debug
	require.NotNil(t, debug)
	require.Equal(t, payload.SelectedModel, debug.Routing.SelectedModel)
	require.Equal(t, payload.SelectionMethod, debug.Routing.ResponseSelectionMethod)
	require.Equal(t, "single", debug.Routing.ModelSelectionMethod)
	require.Equal(t, "code", debug.Decision.Name)
	require.NotNil(t, debug.Decision.Confidence)
	require.Equal(t, 0.91, *debug.Decision.Confidence)
	require.Equal(t, []string{"keyword:a-rule"}, debug.Decision.MatchedRules)
	require.Equal(t, []string{"coding"}, debug.Decision.MatchedKeywords)
	require.Len(t, debug.Decision.CandidateModels, 1)
	require.Equal(t, "Pro", debug.Decision.CandidateModels[0].Model)
	require.Equal(t, []string{"a-rule", "z-rule"}, debug.MatchedSignals.Keyword)
	require.Equal(t, []string{"coding"}, debug.MatchedSignals.KeywordTerms)
	require.NotNil(t, debug.MatchedSignals.Embedding, "empty signal families must serialize as []")
	require.Equal(t, 0.83, debug.SignalConfidences["keyword:a-rule"])
	require.Equal(t, 0.82, debug.SignalValues["keyword:a-rule"])
	require.Equal(t, 0.88, debug.ProjectionScores["score"])
	require.Equal(t, 42, debug.Context.ContextTokens)
	require.Equal(t, 168, debug.Context.ContextTextBytes)
	require.Equal(t, 2, debug.Context.UserMessageCount)
	require.Equal(t, 1, debug.Context.AssistantMessageCount)
	require.Equal(t, 1, debug.Context.ToolDefinitionCount)
	require.True(t, debug.Context.HasDeveloperMessage)
	require.Equal(t, "user", debug.Context.LastMessageRole)
	require.NotNil(t, debug.Timings)
	require.GreaterOrEqual(t, debug.Timings.TotalMs, float64(1000))
	require.Equal(t, 1.235, debug.Timings.RequestParseMs)
	require.Equal(t, 10.988, debug.Timings.SignalEvaluationMs)
	require.Equal(t, 2.346, debug.Timings.Signals.KeywordMs)
	require.Equal(t, []string{"z-rule", "a-rule"}, ctx.VSRMatchedKeywords, "response sorting must not mutate the request context")
	require.Equal(t, 0.826, ctx.VSRSignalConfidences["keyword:a-rule"])
	require.NotContains(t, string(response.GetImmediateResponse().GetBody()), "test-only-placeholder")
}

func TestDecisionOnlyDebugRequestFlow(t *testing.T) {
	for _, tc := range []struct {
		name, model, content, method, reason          string
		noDecisions, noCandidates, defaultMatchesRule bool
	}{
		{name: "rule", model: "auto", content: "coding", method: "routing", reason: "decision_routing_selected_model"},
		{name: "unmatched", model: "auto", content: "hello", method: "fallback", reason: "auto_model_without_selected_model_used_default"},
		{name: "trimmed auto", model: " auto ", content: "hello", method: "fallback", reason: "auto_model_without_selected_model_used_default"},
		{name: "no decisions", model: "auto", content: "coding", method: "fallback", reason: "auto_model_without_selected_model_used_default", noDecisions: true},
		{name: "empty user content", model: "auto", content: "", method: "fallback", reason: "auto_model_without_selected_model_used_default"},
		{name: "no candidates", model: "auto", content: "coding", method: "fallback", reason: "auto_model_without_selected_model_used_default", noCandidates: true},
		{name: "explicit", model: "gateway-type", content: "coding", method: "passthrough", reason: "explicit_model_passthrough"},
		{name: "rule selects same type as default", model: "auto", content: "coding", method: "routing", reason: "decision_routing_selected_model", defaultMatchesRule: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := newDecisionOnlyTestRouter(t)
			router.Config.VLLMEndpoints = nil
			if tc.noDecisions {
				router.Config.Decisions = nil
			}
			if tc.noCandidates {
				router.Config.Decisions[0].ModelRefs = nil
			}
			if tc.defaultMatchesRule {
				router.Config.DefaultModel = "Pro"
			}
			ctx := prepareDecisionOnlyFlow(t, router)
			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q}]}`, tc.model, tc.content)
			response, err := router.handleRequestBody(decisionOnlyBodyRequest(body), ctx)
			require.NoError(t, err)
			require.EqualValues(t, 200, response.GetImmediateResponse().GetStatus().GetCode())
			var payload selectedModelDecisionPayload
			require.NoError(t, json.Unmarshal(response.GetImmediateResponse().GetBody(), &payload))
			debug := payload.Debug
			require.NotNil(t, debug)
			require.Equal(t, tc.method, payload.SelectionMethod)
			require.Equal(t, tc.method, debug.Routing.ResponseSelectionMethod)
			require.Equal(t, payload.SelectedModel, debug.Routing.SelectedModel)
			require.Equal(t, tc.reason, debug.Routing.Reason)
			require.Equal(t, tc.method != "passthrough", debug.Routing.RequestModelIsAuto)
			require.NotNil(t, debug.Timings)
			require.GreaterOrEqual(t, debug.Timings.TotalMs, float64(0))
			require.Equal(t, 1, debug.Context.UserMessageCount)
			require.Equal(t, "user", debug.Context.LastMessageRole)
			if tc.noDecisions || tc.content == "" {
				require.Equal(t, "not_evaluated", debug.PromptCompression.Status)
				require.Nil(t, debug.Timings.Signals)
			} else {
				require.Equal(t, "disabled", debug.PromptCompression.Status)
				if tc.content == "coding" {
					require.Equal(t, []string{"code_keyword"}, debug.MatchedSignals.Keyword)
					require.Equal(t, []string{"coding"}, debug.MatchedSignals.KeywordTerms)
					require.Equal(t, "code", debug.Decision.Name)
					require.Contains(t, debug.Decision.MatchedRules, "keyword:code_keyword")
					require.Contains(t, debug.Decision.MatchedKeywords, "coding")
				} else {
					require.Empty(t, debug.Decision.Name)
					require.Empty(t, debug.Decision.CandidateModels)
				}
			}
			require.Zero(t, ctx.InflightToken)
		})
	}
}

func TestDecisionOnlyDebugEmptyContext(t *testing.T) {
	router := newDecisionOnlyTestRouter(t)
	require.Nil(t, router.buildDecisionOnlyDebugPayload("auto", "Fast", "fallback", nil))
	debug := router.buildDecisionOnlyDebugPayload("auto", "Fast", "fallback", &RequestContext{})
	require.Nil(t, debug.Timings)
	require.Nil(t, debug.Decision.Confidence)
	require.NotNil(t, debug.Decision.CandidateModels)
	require.Empty(t, debug.Decision.CandidateModels)
	require.NotNil(t, debug.Decision.MatchedRules)
	require.NotNil(t, debug.Decision.MatchedKeywords)
	require.Nil(t, debug.SignalConfidences)
}

func TestDecisionOnlyDebugCandidateMetadata(t *testing.T) {
	useReasoning := false
	ctx := &RequestContext{VSRSelectedDecision: &config.Decision{
		Name: "weighted", Tier: 2, Priority: 3, Algorithm: &config.AlgorithmConfig{Type: "static"},
		ModelRefs: []config.ModelRef{{Model: "Pro", LoRAName: "adapter", Weight: 0.7, ModelReasoningControl: config.ModelReasoningControl{UseReasoning: &useReasoning}}},
	}}
	debug := buildDecisionOnlyDecisionDebug(ctx)
	require.Equal(t, "weighted", debug.Name)
	require.Equal(t, 2, debug.Tier)
	require.Equal(t, 3, debug.Priority)
	require.Equal(t, "static", debug.Algorithm)
	require.Equal(t, []decisionOnlyCandidateModelRef{{Model: "Pro", LoRAName: "adapter", Weight: 0.7, UseReasoning: &useReasoning}}, debug.CandidateModels)
}

func TestDecisionOnlyDebugCompression(t *testing.T) {
	longText := strings.Repeat("An independent sentence with several different words. ", 100)
	for _, tc := range []struct {
		name, text, status string
		config             config.PromptCompressionConfig
		applied            bool
	}{
		{name: "disabled", text: "hello", status: "disabled"},
		{name: "missing budget", text: "hello", status: "max_tokens_unset", config: config.PromptCompressionConfig{Enabled: true}},
		{name: "within budget", text: "hello", status: "within_token_budget", config: config.PromptCompressionConfig{Enabled: true, MaxTokens: 100}},
		{name: "below minimum length", text: "hello", status: "below_min_length", config: config.PromptCompressionConfig{Enabled: true, MaxTokens: 1, MinLength: 100}},
		{name: "applied", text: longText, status: "applied", applied: true, config: config.PromptCompressionConfig{Enabled: true, MaxTokens: 32, SkipSignals: []string{"pii", "keyword"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := newDecisionOnlyTestRouter(t)
			router.Config.PromptCompression = tc.config
			ctx := prepareDecisionOnlyFlow(t, router)
			input := router.prepareSignalEvaluationInput(signalConversationHistory{currentUserMessage: tc.text})
			info := input.compressionInfo
			require.Equal(t, tc.status, info.Status)
			require.Equal(t, tc.config.Enabled, info.Enabled)
			require.Equal(t, tc.applied, info.Applied)
			require.GreaterOrEqual(t, info.DurationMs, float64(0))
			if tc.applied {
				require.Less(t, info.CompressedTokens, info.OriginalTokens)
				require.NotEqual(t, tc.text, input.compressedText)
				require.Equal(t, []string{"keyword", "pii"}, info.SkippedSignals)
				require.True(t, input.skipCompressionSignals["keyword"])
			} else {
				require.Equal(t, tc.text, input.compressedText)
				require.Empty(t, input.skipCompressionSignals)
			}
			ctx.VSRPromptCompression = info
			debug := buildDecisionOnlyPromptCompressionDebug(router, ctx)
			require.Equal(t, tc.status, debug.Status)
			require.Equal(t, info.OriginalTokens, debug.OriginalTokens)
			require.Equal(t, info.CompressedTokens, debug.CompressedTokens)
		})
	}
}

func TestDecisionOnlyDebugInvalidNumbersDoNotBreakRouting(t *testing.T) {
	for name, invalid := range map[string]float64{"nan": math.NaN(), "positive infinity": math.Inf(1), "negative infinity": math.Inf(-1)} {
		t.Run(name, func(t *testing.T) {
			router := newDecisionOnlyTestRouter(t)
			router.Config.Decisions[0].ModelRefs[0].Weight = invalid
			ctx := &RequestContext{
				VSRSelectedDecision: &router.Config.Decisions[0], VSRSelectedDecisionName: "code", VSRSelectedDecisionConfidence: 1,
				VSRSignalConfidences: map[string]float64{"valid": 0.9149, "invalid": invalid},
				VSRSignalValues:      map[string]float64{"valid": 1.234, "invalid": invalid, "large": math.MaxFloat64},
				VSRProjectionScores:  map[string]float64{"valid": 0.876, "invalid": invalid},
				VSRDecisionTimings:   requestDecisionTimingInfo{RequestParseMs: invalid},
				VSRSignalMetrics:     &classification.SignalMetricsCollection{Keyword: classification.SignalMetrics{ExecutionTimeMs: invalid}},
				VSRPromptCompression: promptCompressionRuntimeInfo{Enabled: true, Applied: true, Status: "applied", Ratio: invalid},
			}
			response := router.buildDecisionOnlySelectedModelResponse("auto", requestDecisionState{decisionName: "code", selectedModel: "Pro"}, ctx)
			require.EqualValues(t, 200, response.GetImmediateResponse().GetStatus().GetCode(), "optional debug numbers must not turn a valid decision into an error")
			var payload selectedModelDecisionPayload
			require.NoError(t, json.Unmarshal(response.GetImmediateResponse().GetBody(), &payload))
			require.Equal(t, "Pro", payload.SelectedModel)
			require.Equal(t, 0.91, payload.Debug.SignalConfidences["valid"])
			require.Equal(t, 1.23, payload.Debug.SignalValues["valid"])
			require.Equal(t, math.MaxFloat64, payload.Debug.SignalValues["large"])
			require.Equal(t, 0.88, payload.Debug.ProjectionScores["valid"])
			require.NotContains(t, payload.Debug.SignalConfidences, "invalid")
			require.NotContains(t, payload.Debug.SignalValues, "invalid")
			require.NotContains(t, payload.Debug.ProjectionScores, "invalid")
			require.Zero(t, payload.Debug.Decision.CandidateModels[0].Weight)
			require.Zero(t, payload.Debug.PromptCompression.Ratio)
			require.Nil(t, payload.Debug.Timings)
		})
	}
}

func TestDecisionOnlyDebugPreservesNormalRouting(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("decision_only=%v", enabled), func(t *testing.T) {
			router := newDecisionOnlyTestRouter(t)
			router.Config.DecisionOnlyMode = enabled
			router.Config.PromptCompression = config.PromptCompressionConfig{Enabled: true, MaxTokens: 32, SkipSignals: []string{"keyword"}}
			ctx := prepareDecisionOnlyFlow(t, router)
			body, err := json.Marshal(map[string]interface{}{"model": "auto", "messages": []map[string]string{{"role": "user", "content": "coding. " + strings.Repeat("An independent sentence with several different words. ", 100)}}})
			require.NoError(t, err)
			response, err := router.handleRequestBody(decisionOnlyBodyRequest(string(body)), ctx)
			t.Cleanup(func() { inflight.End(ctx.RequestModel, ctx.InflightToken) })
			require.NoError(t, err)
			require.Equal(t, "Pro", ctx.VSRSelectedModel)
			require.True(t, ctx.VSRPromptCompression.Applied)
			var payload map[string]interface{}
			if enabled {
				require.NotNil(t, response.GetImmediateResponse())
				require.NoError(t, json.Unmarshal(response.GetImmediateResponse().GetBody(), &payload))
				require.Equal(t, "Pro", payload["selected_model"])
				require.Contains(t, payload, "debug")
			} else {
				require.Nil(t, response.GetImmediateResponse())
				require.NotNil(t, response.GetRequestBody())
				require.NoError(t, json.Unmarshal(response.GetRequestBody().GetResponse().GetBodyMutation().GetBody(), &payload))
				require.Equal(t, "Pro", payload["model"])
				require.NotContains(t, payload, "debug")
			}
		})
	}
}
