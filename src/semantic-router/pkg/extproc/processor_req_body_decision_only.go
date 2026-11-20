package extproc

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"

	core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	ext_proc "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"

	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/headers"
)

const (
	decisionOnlyPayloadSchemaVersion = "v1"
	decisionOnlyMethodRouting        = "routing"
	decisionOnlyMethodFallback       = "fallback"
	decisionOnlyMethodPassthrough    = "passthrough"
)

type selectedModelDecisionPayload struct {
	SchemaVersion   string   `json:"schema_version"`
	SelectionMethod string   `json:"selection_method"`
	SelectedModel   string   `json:"selected_model"`
	Decision        string   `json:"decision,omitempty"`
	Category        string   `json:"category,omitempty"`
	Confidence      *float64 `json:"confidence,omitempty"`
}

func (r *OpenAIRouter) decisionOnlyModeEnabled() bool {
	return r != nil && r.Config != nil && r.Config.DecisionOnlyMode
}

func (r *OpenAIRouter) buildDecisionOnlySelectedModelResponse(
	originalModel string,
	state requestDecisionState,
	ctx *RequestContext,
) *ext_proc.ProcessingResponse {
	selectedModel, selectionMethod, statusCode, message := r.resolveDecisionOnlySelectedModel(originalModel, state, ctx)
	if statusCode != 0 {
		return r.createErrorResponse(statusCode, message)
	}

	payload := selectedModelDecisionPayload{
		SchemaVersion:   decisionOnlyPayloadSchemaVersion,
		SelectionMethod: selectionMethod,
		SelectedModel:   selectedModel,
		Decision:        state.decisionName,
	}
	setHeaders := []*core.HeaderValueOption{
		newHeaderValueOption("content-type", "application/json"),
		newHeaderValueOption(headers.SelectedModel, selectedModel),
		newHeaderValueOption(headers.VSRSelectedModel, selectedModel),
	}
	if ctx != nil {
		payload.Category = ctx.VSRSelectedCategory
		if state.decisionName != "" {
			appendOptionalHeader(&setHeaders, headers.VSRSelectedDecision, state.decisionName)
			if ctx.VSRSelectedDecisionConfidence >= 0 {
				confidence := math.Round(ctx.VSRSelectedDecisionConfidence*100) / 100
				payload.Confidence = &confidence
				appendOptionalHeader(&setHeaders, headers.VSRSelectedConfidence, fmt.Sprintf("%.4f", ctx.VSRSelectedDecisionConfidence))
			}
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return r.createErrorResponse(http.StatusInternalServerError, "failed to build decision-only response")
	}
	return &ext_proc.ProcessingResponse{
		Response: &ext_proc.ProcessingResponse_ImmediateResponse{
			ImmediateResponse: &ext_proc.ImmediateResponse{
				Status:  &typev3.HttpStatus{Code: typev3.StatusCode_OK},
				Headers: &ext_proc.HeaderMutation{SetHeaders: setHeaders},
				Body:    body,
			},
		},
	}
}

func (r *OpenAIRouter) resolveDecisionOnlySelectedModel(
	originalModel string,
	state requestDecisionState,
	ctx *RequestContext,
) (selectedModel string, selectionMethod string, statusCode int, message string) {
	originalModel = strings.TrimSpace(originalModel)
	if originalModel == "" {
		return "", decisionOnlyMethodPassthrough, http.StatusBadRequest, "model is required"
	}
	selectedModel, selectionMethod = originalModel, decisionOnlyMethodPassthrough
	if r.Config.IsAutoModelName(originalModel) {
		selectedModel = strings.TrimSpace(state.selectedModel)
		selectionMethod = decisionOnlyMethodRouting
		// The evaluation stage can already have returned DefaultModel. Its
		// presence alone does not mean a routing decision selected the model.
		if state.decisionName == "" || selectedModel == "" || (ctx != nil && ctx.VSRSelectionMethod == "default") {
			selectedModel = strings.TrimSpace(r.Config.DefaultModel)
			selectionMethod = decisionOnlyMethodFallback
		}
		if selectedModel == "" {
			return "", selectionMethod, http.StatusBadRequest, "unable to route request: no model selected and no default model configured"
		}
	}
	return selectedModel, selectionMethod, 0, ""
}
