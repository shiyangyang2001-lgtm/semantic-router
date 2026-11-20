package extproc

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	ext_proc "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"

	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/cache"
	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/classification"
	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/config"
	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/inflight"
)

func prepareDecisionOnlyFlow(t *testing.T, router *OpenAIRouter) *RequestContext {
	t.Helper()
	classifier, err := classification.NewClassifier(router.Config, nil, nil, nil)
	require.NoError(t, err)
	router.Classifier = classifier
	router.CredentialResolver = buildDefaultCredentialResolver(router.Config, false)
	if !router.Config.DecisionOnlyMode {
		router.Cache = cache.NewInMemoryCache(cache.InMemoryCacheOptions{Enabled: false})
	}
	return &RequestContext{
		Headers:      map[string]string{":path": "/v1/chat/completions"},
		TraceContext: context.Background(),
	}
}

func TestDecisionOnlyRequestFlow(t *testing.T) {
	for _, tc := range []struct {
		name, model, content, wantModel, wantMethod, wantDecision string
		noDecisions, noDefault, defaultInDecision, noEndpoints    bool
	}{
		{name: "keyword selects Pro", model: "auto", content: "help with coding", wantModel: "Pro", wantMethod: "routing", wantDecision: "code"},
		{name: "unmatched rule uses default", model: "auto", content: "hello", wantModel: "Fast", wantMethod: "fallback"},
		{name: "no decisions uses default", model: "MoM", content: "coding", noDecisions: true, wantModel: "Fast", wantMethod: "fallback"},
		{name: "explicit model preserved despite rule", model: "Fast", content: "coding", wantModel: "Fast", wantMethod: "passthrough", wantDecision: "code"},
		{name: "matched decision without model uses default", model: "auto", content: "coding", defaultInDecision: true, wantModel: "Fast", wantMethod: "fallback", wantDecision: "code"},
		{name: "no default returns error", model: "auto", content: "hello", noDefault: true},
		{name: "rule returns type without endpoints", model: "auto", content: "coding", noEndpoints: true, wantModel: "Pro", wantMethod: "routing", wantDecision: "code"},
		{name: "fallback returns type without endpoints", model: "auto", content: "hello", noEndpoints: true, wantModel: "Fast", wantMethod: "fallback"},
		{name: "explicit gateway type without endpoints", model: "gateway-pro", content: "hello", noEndpoints: true, wantModel: "gateway-pro", wantMethod: "passthrough"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := newDecisionOnlyTestRouter(t)
			if tc.noDecisions {
				router.Config.Decisions = nil
			}
			if tc.noDefault {
				router.Config.DefaultModel = ""
			}
			if tc.noEndpoints {
				router.Config.VLLMEndpoints = nil
			}
			if tc.defaultInDecision {
				router.Config.Decisions[0].ModelRefs = nil
			}
			ctx := prepareDecisionOnlyFlow(t, router)
			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q}]}`, tc.model, tc.content)
			response, err := router.handleRequestBody(decisionOnlyBodyRequest(body), ctx)
			require.NoError(t, err)
			require.NotNil(t, response.GetImmediateResponse(), "decision-only must not continue toward upstream routing")
			require.Nil(t, response.GetRequestBody())
			require.Zero(t, ctx.InflightToken, "decision-only must not start a backend inference request")
			if tc.noDefault {
				require.EqualValues(t, 400, response.GetImmediateResponse().GetStatus().GetCode())
				return
			}
			require.EqualValues(t, 200, response.GetImmediateResponse().GetStatus().GetCode())
			var payload selectedModelDecisionPayload
			require.NoError(t, json.Unmarshal(response.GetImmediateResponse().GetBody(), &payload))
			require.Equal(t, tc.wantModel, payload.SelectedModel)
			require.Equal(t, tc.wantMethod, payload.SelectionMethod)
			require.Equal(t, tc.wantDecision, payload.Decision)
		})
	}
}

func TestDecisionOnlyDisabledContinuesRouting(t *testing.T) {
	router := newDecisionOnlyTestRouter(t)
	router.Config.DecisionOnlyMode = false
	ctx := prepareDecisionOnlyFlow(t, router)
	response, err := router.handleRequestBody(decisionOnlyBodyRequest(`{"model":"Pro","messages":[{"role":"user","content":"hello"}]}`), ctx)
	t.Cleanup(func() { inflight.End(ctx.RequestModel, ctx.InflightToken) })
	require.NoError(t, err)
	require.Nil(t, response.GetImmediateResponse())
	require.NotNil(t, response.GetRequestBody(), "disabled mode must keep the normal routing response")
}

func TestDecisionOnlySkipsPostDecisionPlugins(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%v", enabled), func(t *testing.T) {
			router := newDecisionOnlyTestRouter(t)
			router.Config.DecisionOnlyMode = enabled
			router.Config.Decisions[0].Plugins = []config.DecisionPlugin{{
				Type: "fast_response", Configuration: config.MustStructuredPayload(map[string]interface{}{"message": "plugin-marker"}),
			}}
			ctx := prepareDecisionOnlyFlow(t, router)
			response, err := router.handleRequestBody(decisionOnlyBodyRequest(`{"model":"auto","messages":[{"role":"user","content":"coding"}]}`), ctx)
			require.NoError(t, err)
			require.NotNil(t, response.GetImmediateResponse())
			body := string(response.GetImmediateResponse().GetBody())
			if enabled {
				require.Contains(t, body, `"selected_model":"Pro"`)
				require.NotContains(t, body, "plugin-marker")
			} else {
				require.Contains(t, body, "plugin-marker")
			}
		})
	}
}

func TestDecisionOnlyRejectsInvalidRequests(t *testing.T) {
	for _, body := range []string{
		`{`,
		`{"model":"auto"}`,
		`{"model":"auto","messages":"invalid"}`,
		`{"messages":[{"role":"user","content":"hello"}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			router := newDecisionOnlyTestRouter(t)
			ctx := prepareDecisionOnlyFlow(t, router)
			response, err := router.handleRequestBody(decisionOnlyBodyRequest(body), ctx)
			require.NoError(t, err)
			require.EqualValues(t, 400, response.GetImmediateResponse().GetStatus().GetCode())
		})
	}
}

func TestDecisionOnlyPreservesSkipProcessingOptOut(t *testing.T) {
	for _, gate := range []bool{false, true} {
		t.Run(fmt.Sprintf("gate=%v", gate), func(t *testing.T) {
			router := newDecisionOnlyTestRouter(t)
			router.Config.SkipProcessing.Enabled = gate
			ctx := prepareDecisionOnlyFlow(t, router)
			_, err := router.handleRequestHeaders(&ext_proc.ProcessingRequest_RequestHeaders{RequestHeaders: &ext_proc.HttpHeaders{
				Headers: &core.HeaderMap{Headers: []*core.HeaderValue{
					{Key: ":method", Value: "POST"}, {Key: ":path", Value: "/v1/chat/completions"},
					{Key: "x-vsr-skip-processing", Value: "true"},
				}},
			}}, ctx)
			require.NoError(t, err)
			response, err := router.handleRequestBody(decisionOnlyBodyRequest(`{"model":"Pro","messages":[{"role":"user","content":"hello"}]}`), ctx)
			require.NoError(t, err)
			require.Equal(t, gate, ctx.SkipProcessing)
			if gate {
				require.Nil(t, response.GetImmediateResponse())
				require.NotNil(t, response.GetRequestBody())
			} else {
				require.NotNil(t, response.GetImmediateResponse())
			}
		})
	}
}
