package extproc

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/classification"
)

func decisionOnlyConversationFacts(history signalConversationHistory) classification.ConversationFacts {
	return classification.ConversationFacts{
		HasDeveloperMessage:     history.hasDeveloperMessage,
		UserMessageCount:        history.userMessageCount,
		AssistantMessageCount:   history.assistantMessageCount,
		SystemMessageCount:      history.systemMessageCount,
		ToolMessageCount:        history.toolMessageCount,
		ToolDefinitionCount:     history.toolDefinitionCount,
		AssistantToolCallCount:  history.assistantToolCallCount,
		ToolResultCount:         history.toolResultCount,
		LastMessageRole:         history.lastMessageRole,
		LastMessageToolResult:   history.lastMessageToolResult,
		LastUserAfterToolResult: history.lastUserAfterToolResult,
	}
}

func elapsedMilliseconds(start time.Time) float64 {
	if start.IsZero() {
		return 0
	}
	return roundMilliseconds(float64(time.Since(start).Microseconds()) / 1000)
}

func roundMilliseconds(value float64) float64 {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	scaled := value * 1000
	if math.IsInf(scaled, 0) {
		return value
	}
	return math.Round(scaled) / 1000
}

func buildDecisionOnlyTimingsDebug(ctx *RequestContext) *decisionOnlyTimingsDebug {
	if ctx == nil {
		return nil
	}
	timings := ctx.VSRDecisionTimings
	out := &decisionOnlyTimingsDebug{
		TotalMs:              elapsedMilliseconds(ctx.ProcessingStartTime),
		RequestParseMs:       roundMilliseconds(timings.RequestParseMs),
		PreRoutingMs:         roundMilliseconds(timings.PreRoutingMs),
		SignalInputMs:        roundMilliseconds(timings.SignalInputMs),
		PromptCompressionMs:  roundMilliseconds(timings.PromptCompressionMs),
		SignalEvaluationMs:   roundMilliseconds(timings.SignalEvaluationMs),
		DecisionEvaluationMs: roundMilliseconds(timings.DecisionEvaluationMs),
		ModelSelectionMs:     roundMilliseconds(timings.ModelSelectionMs),
		Signals:              buildDecisionOnlySignalTimingsDebug(ctx),
	}
	if *out == (decisionOnlyTimingsDebug{}) {
		return nil
	}
	return out
}

func buildDecisionOnlySignalTimingsDebug(ctx *RequestContext) *decisionOnlySignalTimingsDebug {
	if ctx == nil || ctx.VSRSignalMetrics == nil {
		return nil
	}
	metrics := ctx.VSRSignalMetrics
	out := &decisionOnlySignalTimingsDebug{
		KeywordMs:      roundMilliseconds(metrics.Keyword.ExecutionTimeMs),
		EmbeddingMs:    roundMilliseconds(metrics.Embedding.ExecutionTimeMs),
		DomainMs:       roundMilliseconds(metrics.Domain.ExecutionTimeMs),
		FactCheckMs:    roundMilliseconds(metrics.FactCheck.ExecutionTimeMs),
		UserFeedbackMs: roundMilliseconds(metrics.UserFeedback.ExecutionTimeMs),
		ReaskMs:        roundMilliseconds(metrics.Reask.ExecutionTimeMs),
		PreferenceMs:   roundMilliseconds(metrics.Preference.ExecutionTimeMs),
		LanguageMs:     roundMilliseconds(metrics.Language.ExecutionTimeMs),
		ContextMs:      roundMilliseconds(metrics.Context.ExecutionTimeMs),
		StructureMs:    roundMilliseconds(metrics.Structure.ExecutionTimeMs),
		ComplexityMs:   roundMilliseconds(metrics.Complexity.ExecutionTimeMs),
		ModalityMs:     roundMilliseconds(metrics.Modality.ExecutionTimeMs),
		AuthzMs:        roundMilliseconds(metrics.Authz.ExecutionTimeMs),
		JailbreakMs:    roundMilliseconds(metrics.Jailbreak.ExecutionTimeMs),
		PIIMs:          roundMilliseconds(metrics.PII.ExecutionTimeMs),
		KBMs:           roundMilliseconds(metrics.KB.ExecutionTimeMs),
		ConversationMs: roundMilliseconds(metrics.Conversation.ExecutionTimeMs),
		EventMs:        roundMilliseconds(metrics.Event.ExecutionTimeMs),
	}
	if *out == (decisionOnlySignalTimingsDebug{}) {
		return nil
	}
	return out
}

func (r *OpenAIRouter) buildDecisionOnlyDebugPayload(
	originalModel string,
	selectedModel string,
	selectionMethod string,
	ctx *RequestContext,
) *decisionOnlyDebugPayload {
	if ctx == nil {
		return nil
	}
	return &decisionOnlyDebugPayload{
		Routing:           r.buildDecisionOnlyRoutingDebug(originalModel, selectedModel, selectionMethod, ctx),
		PromptCompression: buildDecisionOnlyPromptCompressionDebug(r, ctx),
		Timings:           buildDecisionOnlyTimingsDebug(ctx),
		Decision:          buildDecisionOnlyDecisionDebug(ctx),
		MatchedSignals:    buildDecisionOnlyMatchedSignalsDebug(ctx),
		SignalConfidences: cloneDecisionOnlyFloat64Map(ctx.VSRSignalConfidences),
		SignalValues:      cloneDecisionOnlyFloat64Map(ctx.VSRSignalValues),
		ProjectionScores:  cloneDecisionOnlyFloat64Map(ctx.VSRProjectionScores),
		Context:           buildDecisionOnlyContextDebug(ctx),
	}
}

func (r *OpenAIRouter) buildDecisionOnlyRoutingDebug(
	originalModel string,
	selectedModel string,
	selectionMethod string,
	ctx *RequestContext,
) decisionOnlyRoutingDebug {
	out := decisionOnlyRoutingDebug{
		OriginalRequestModel:    strings.TrimSpace(originalModel),
		SelectedModel:           selectedModel,
		ResponseSelectionMethod: selectionMethod,
		ModelSelectionMethod:    ctx.VSRSelectionMethod,
		ReasoningMode:           ctx.VSRReasoningMode,
	}
	if r != nil && r.Config != nil {
		out.RequestModelIsAuto = r.Config.IsAutoModelName(out.OriginalRequestModel)
		out.DefaultModel = strings.TrimSpace(r.Config.DefaultModel)
	}
	switch selectionMethod {
	case decisionOnlyMethodRouting:
		out.Reason = "decision_routing_selected_model"
	case decisionOnlyMethodFallback:
		out.Reason = "auto_model_without_selected_model_used_default"
	case decisionOnlyMethodPassthrough:
		out.Reason = "explicit_model_passthrough"
	default:
		out.Reason = "unknown"
	}
	return out
}

func buildDecisionOnlyPromptCompressionDebug(
	r *OpenAIRouter,
	ctx *RequestContext,
) decisionOnlyPromptCompressionDebug {
	info := ctx.VSRPromptCompression
	if info.Status == "" {
		info.Status = "not_evaluated"
		if r != nil && r.Config != nil {
			info.Enabled = r.Config.PromptCompression.Enabled
			info.Profile = r.Config.PromptCompression.NormalizedProfile()
			info.MaxTokens = r.Config.PromptCompression.MaxTokens
			info.MinLength = r.Config.PromptCompression.MinLength
		}
	}
	ratio := math.Round(info.Ratio*100) / 100
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		ratio = 0
	}
	return decisionOnlyPromptCompressionDebug{
		Enabled:          info.Enabled,
		Applied:          info.Applied,
		Status:           info.Status,
		Profile:          info.Profile,
		MaxTokens:        info.MaxTokens,
		MinLength:        info.MinLength,
		OriginalTokens:   info.OriginalTokens,
		CompressedTokens: info.CompressedTokens,
		Ratio:            ratio,
		SkippedSignals:   cloneDecisionOnlyStringSlice(info.SkippedSignals),
	}
}

func buildDecisionOnlyDecisionDebug(ctx *RequestContext) decisionOnlyDecisionDebug {
	decisionName := decisionOnlyDecisionName(ctx)
	out := decisionOnlyDecisionDebug{
		Name:            decisionName,
		Category:        ctx.VSRSelectedCategory,
		CandidateModels: buildDecisionOnlyCandidateModelRefs(ctx),
		MatchedRules:    cloneDecisionOnlyStringSlice(ctx.VSRDecisionMatchedRules),
		MatchedKeywords: cloneDecisionOnlyStringSlice(ctx.VSRDecisionMatchedKeywords),
	}
	if decisionName != "" && ctx.VSRSelectedDecisionConfidence >= 0 {
		confidence := math.Round(ctx.VSRSelectedDecisionConfidence*100) / 100
		out.Confidence = &confidence
	}
	if ctx.VSRSelectedDecision == nil {
		return out
	}
	out.Tier = ctx.VSRSelectedDecision.Tier
	out.Priority = ctx.VSRSelectedDecision.Priority
	if ctx.VSRSelectedDecision.Algorithm != nil {
		out.Algorithm = ctx.VSRSelectedDecision.Algorithm.Type
	}
	return out
}

func buildDecisionOnlyCandidateModelRefs(ctx *RequestContext) []decisionOnlyCandidateModelRef {
	if ctx == nil || ctx.VSRSelectedDecision == nil || len(ctx.VSRSelectedDecision.ModelRefs) == 0 {
		return []decisionOnlyCandidateModelRef{}
	}
	out := make([]decisionOnlyCandidateModelRef, 0, len(ctx.VSRSelectedDecision.ModelRefs))
	for _, modelRef := range ctx.VSRSelectedDecision.ModelRefs {
		weight := modelRef.Weight
		if math.IsNaN(weight) || math.IsInf(weight, 0) {
			weight = 0
		}
		out = append(out, decisionOnlyCandidateModelRef{
			Model:        modelRef.Model,
			LoRAName:     modelRef.LoRAName,
			Weight:       weight,
			UseReasoning: modelRef.UseReasoning,
		})
	}
	return out
}

func buildDecisionOnlyMatchedSignalsDebug(ctx *RequestContext) decisionOnlyMatchedSignalsDebug {
	return decisionOnlyMatchedSignalsDebug{
		Keyword:      cloneDecisionOnlyStringSlice(ctx.VSRMatchedKeywords),
		KeywordTerms: cloneDecisionOnlyStringSlice(ctx.VSRMatchedKeywordTerms),
		Embedding:    cloneDecisionOnlyStringSlice(ctx.VSRMatchedEmbeddings),
		Domain:       cloneDecisionOnlyStringSlice(ctx.VSRMatchedDomains),
		FactCheck:    cloneDecisionOnlyStringSlice(ctx.VSRMatchedFactCheck),
		UserFeedback: cloneDecisionOnlyStringSlice(ctx.VSRMatchedUserFeedback),
		Reask:        cloneDecisionOnlyStringSlice(ctx.VSRMatchedReask),
		Preference:   cloneDecisionOnlyStringSlice(ctx.VSRMatchedPreference),
		Language:     cloneDecisionOnlyStringSlice(ctx.VSRMatchedLanguage),
		Context:      cloneDecisionOnlyStringSlice(ctx.VSRMatchedContext),
		Structure:    cloneDecisionOnlyStringSlice(ctx.VSRMatchedStructure),
		Complexity:   cloneDecisionOnlyStringSlice(ctx.VSRMatchedComplexity),
		Modality:     cloneDecisionOnlyStringSlice(ctx.VSRMatchedModality),
		Authz:        cloneDecisionOnlyStringSlice(ctx.VSRMatchedAuthz),
		Jailbreak:    cloneDecisionOnlyStringSlice(ctx.VSRMatchedJailbreak),
		PII:          cloneDecisionOnlyStringSlice(ctx.VSRMatchedPII),
		KB:           cloneDecisionOnlyStringSlice(ctx.VSRMatchedKB),
		Conversation: cloneDecisionOnlyStringSlice(ctx.VSRMatchedConversation),
		Event:        cloneDecisionOnlyStringSlice(ctx.VSRMatchedEvent),
		Projection:   cloneDecisionOnlyStringSlice(ctx.VSRMatchedProjection),
	}
}

func buildDecisionOnlyContextDebug(ctx *RequestContext) decisionOnlyContextDebug {
	facts := ctx.VSRConversationFacts
	return decisionOnlyContextDebug{
		ContextTokens:          ctx.VSRContextTokenCount,
		ContextTextBytes:       ctx.VSRContextTextBytes,
		UserMessageCount:       facts.UserMessageCount,
		AssistantMessageCount:  facts.AssistantMessageCount,
		SystemMessageCount:     facts.SystemMessageCount,
		ToolMessageCount:       facts.ToolMessageCount,
		ToolDefinitionCount:    facts.ToolDefinitionCount,
		AssistantToolCallCount: facts.AssistantToolCallCount,
		ToolResultCount:        facts.ToolResultCount,
		LastMessageRole:        facts.LastMessageRole,
		HasDeveloperMessage:    facts.HasDeveloperMessage,
	}
}

func cloneDecisionOnlyStringSlice(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func cloneDecisionOnlyFloat64Map(values map[string]float64) map[string]float64 {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]float64, len(values))
	for key, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		scaled := value * 100
		if !math.IsInf(scaled, 0) {
			value = math.Round(scaled) / 100
		}
		out[key] = value
	}
	return out
}

func decisionOnlyDecisionName(ctx *RequestContext) string {
	if ctx.VSRSelectedDecisionName != "" {
		return ctx.VSRSelectedDecisionName
	}
	if ctx.VSRSelectedDecision != nil {
		return ctx.VSRSelectedDecision.Name
	}
	return ""
}
