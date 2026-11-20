package extproc

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	ext_proc "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"

	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/config"
	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/headers"
)

func newDecisionOnlyTestRouter(t *testing.T) *OpenAIRouter {
	t.Helper()
	cfg, err := config.ParseYAMLBytes([]byte(`
version: v0.3
listeners: []
providers:
  defaults:
    default_model: Fast
  models:
    - name: Fast
      backend_refs:
        - endpoint: 127.0.0.1:9999
          api_key: test-only-placeholder
    - name: Pro
      backend_refs:
        - endpoint: 127.0.0.1:9999
          api_key: test-only-placeholder
routing:
  modelCards:
    - name: Fast
    - name: Pro
  signals:
    keywords:
      - name: code_keyword
        operator: OR
        keywords: [coding]
  decisions:
    - name: code
      priority: 1
      rules:
        operator: AND
        conditions:
          - type: keyword
            name: code_keyword
      modelRefs:
        - model: Pro
global:
  router:
    decision_only_mode: true
`))
	require.NoError(t, err)
	return &OpenAIRouter{Config: cfg}
}

func TestDecisionOnlyModeEnabled(t *testing.T) {
	for _, tc := range []struct {
		name   string
		router *OpenAIRouter
		want   bool
	}{
		{name: "nil router"},
		{name: "nil config", router: &OpenAIRouter{}},
		{name: "disabled", router: &OpenAIRouter{Config: &config.RouterConfig{}}},
		{name: "enabled", router: newDecisionOnlyTestRouter(t), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.router.decisionOnlyModeEnabled())
		})
	}
}

func TestDecisionOnlyModelResolution(t *testing.T) {
	for _, tc := range []struct {
		name, original, selected, decision, runtimeMethod string
		noDefault, noEndpoints                            bool
		wantModel, wantMethod, wantError                  string
	}{
		{name: "rule selected model", original: "MoM", selected: "Pro", decision: "code", wantModel: "Pro", wantMethod: "routing"},
		{name: "rule selects same model as default", original: "auto", selected: "Fast", decision: "simple", wantModel: "Fast", wantMethod: "routing"},
		{name: "default already returned by evaluation", original: "auto", selected: "Fast", wantModel: "Fast", wantMethod: "fallback"},
		{name: "no selection uses default", original: "auto", wantModel: "Fast", wantMethod: "fallback"},
		{name: "matched decision defaults model", original: "auto", selected: "Fast", decision: "code", runtimeMethod: "default", wantModel: "Fast", wantMethod: "fallback"},
		{name: "explicit model preserved", original: "Pro", wantModel: "Pro", wantMethod: "passthrough"},
		{name: "explicit model overrides routing state", original: "Fast", selected: "Pro", decision: "code", wantModel: "Fast", wantMethod: "passthrough"},
		{name: "trim explicit model", original: " Pro ", wantModel: "Pro", wantMethod: "passthrough"},
		{name: "explicit gateway type needs no backend", original: "gateway-pro", wantModel: "gateway-pro", wantMethod: "passthrough"},
		{name: "selected gateway type needs no backend", original: "auto", selected: "gateway-pro", decision: "code", wantModel: "gateway-pro", wantMethod: "routing"},
		{name: "missing model", wantError: "model is required"},
		{name: "no default", original: "auto", noDefault: true, wantError: "no default model"},
		{name: "selected type without endpoints", original: "auto", selected: "Pro", decision: "code", noEndpoints: true, wantModel: "Pro", wantMethod: "routing"},
		{name: "default type without endpoints", original: "auto", noEndpoints: true, wantModel: "Fast", wantMethod: "fallback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := newDecisionOnlyTestRouter(t)
			if tc.noDefault {
				router.Config.DefaultModel = ""
			}
			if tc.noEndpoints {
				router.Config.VLLMEndpoints = nil
			}
			ctx := &RequestContext{VSRSelectionMethod: tc.runtimeMethod}
			model, method, status, message := router.resolveDecisionOnlySelectedModel(tc.original, requestDecisionState{
				selectedModel: tc.selected, decisionName: tc.decision,
			}, ctx)
			if tc.wantError != "" {
				require.Equal(t, 400, status)
				require.Contains(t, message, tc.wantError)
				return
			}
			require.Zero(t, status, message)
			require.Equal(t, tc.wantModel, model)
			require.Equal(t, tc.wantMethod, method)
		})
	}
}

func TestDecisionOnlyResponseContract(t *testing.T) {
	router := newDecisionOnlyTestRouter(t)
	for _, confidence := range []float64{0, 0.9149, -1} {
		ctx := &RequestContext{TraceContext: context.Background(), VSRSelectedCategory: "coding", VSRSelectedDecisionConfidence: confidence}
		response := router.buildDecisionOnlySelectedModelResponse("auto", requestDecisionState{
			decisionName: "code", selectedModel: "Pro",
		}, ctx)
		immediate := response.GetImmediateResponse()
		require.NotNil(t, immediate)
		require.EqualValues(t, 200, immediate.GetStatus().GetCode())
		var payload selectedModelDecisionPayload
		require.NoError(t, json.Unmarshal(immediate.GetBody(), &payload))
		require.Equal(t, "v1", payload.SchemaVersion)
		require.Equal(t, "Pro", payload.SelectedModel)
		require.Equal(t, "routing", payload.SelectionMethod)
		require.Equal(t, "code", payload.Decision)
		require.Equal(t, "coding", payload.Category)
		if confidence < 0 {
			require.Nil(t, payload.Confidence)
		} else {
			require.NotNil(t, payload.Confidence)
			require.Equal(t, math.Round(confidence*100)/100, *payload.Confidence)
		}
		headerMap := headerValuesByName(immediate.GetHeaders().GetSetHeaders())
		require.Equal(t, "application/json", headerMap["content-type"])
		require.Equal(t, "Pro", headerMap[headers.SelectedModel])
		require.Equal(t, "Pro", headerMap[headers.VSRSelectedModel])
		require.NotContains(t, headerMap, "x-vsr-schema-version")
		require.NotContains(t, headerMap, "x-vsr-response-path")
	}
}

func TestDecisionOnlyResponseErrorsAndFallback(t *testing.T) {
	router := newDecisionOnlyTestRouter(t)
	response := router.buildDecisionOnlySelectedModelResponse("", requestDecisionState{}, nil)
	require.EqualValues(t, 400, response.GetImmediateResponse().GetStatus().GetCode())
	require.Contains(t, string(response.GetImmediateResponse().GetBody()), "model is required")
	response = router.buildDecisionOnlySelectedModelResponse("auto", requestDecisionState{selectedModel: "Fast"}, nil)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(response.GetImmediateResponse().GetBody(), &payload))
	require.Equal(t, "fallback", payload["selection_method"])
	require.NotContains(t, payload, "decision")
	require.NotContains(t, payload, "confidence")
	require.NotContains(t, payload, "debug")
	response = router.buildDecisionOnlySelectedModelResponse("auto", requestDecisionState{decisionName: "code", selectedModel: "Pro"}, &RequestContext{VSRSelectedDecisionConfidence: math.Inf(1)})
	require.EqualValues(t, 500, response.GetImmediateResponse().GetStatus().GetCode())
}

func decisionOnlyBodyRequest(body string) *ext_proc.ProcessingRequest_RequestBody {
	return &ext_proc.ProcessingRequest_RequestBody{RequestBody: &ext_proc.HttpBody{Body: []byte(body), EndOfStream: true}}
}
