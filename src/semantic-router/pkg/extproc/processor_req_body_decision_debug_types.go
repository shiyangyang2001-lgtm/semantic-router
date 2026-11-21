package extproc

type decisionOnlyDebugPayload struct {
	Routing           decisionOnlyRoutingDebug           `json:"routing"`
	PromptCompression decisionOnlyPromptCompressionDebug `json:"prompt_compression"`
	Timings           *decisionOnlyTimingsDebug          `json:"timings,omitempty"`
	Decision          decisionOnlyDecisionDebug          `json:"decision"`
	MatchedSignals    decisionOnlyMatchedSignalsDebug    `json:"matched_signals"`
	SignalConfidences map[string]float64                 `json:"signal_confidences,omitempty"`
	SignalValues      map[string]float64                 `json:"signal_values,omitempty"`
	ProjectionScores  map[string]float64                 `json:"projection_scores,omitempty"`
	Context           decisionOnlyContextDebug           `json:"context"`
}

type decisionOnlyRoutingDebug struct {
	OriginalRequestModel    string `json:"original_request_model,omitempty"`
	RequestModelIsAuto      bool   `json:"request_model_is_auto"`
	DefaultModel            string `json:"default_model,omitempty"`
	SelectedModel           string `json:"selected_model"`
	ResponseSelectionMethod string `json:"response_selection_method"`
	ModelSelectionMethod    string `json:"model_selection_method,omitempty"`
	ReasoningMode           string `json:"reasoning_mode,omitempty"`
	Reason                  string `json:"reason"`
}

type decisionOnlyPromptCompressionDebug struct {
	Enabled          bool     `json:"enabled"`
	Applied          bool     `json:"applied"`
	Status           string   `json:"status"`
	Profile          string   `json:"profile,omitempty"`
	MaxTokens        int      `json:"max_tokens,omitempty"`
	MinLength        int      `json:"min_length,omitempty"`
	OriginalTokens   int      `json:"original_tokens,omitempty"`
	CompressedTokens int      `json:"compressed_tokens,omitempty"`
	Ratio            float64  `json:"ratio,omitempty"`
	SkippedSignals   []string `json:"skipped_signals,omitempty"`
}

type decisionOnlyTimingsDebug struct {
	TotalMs              float64                         `json:"total_ms,omitempty"`
	RequestParseMs       float64                         `json:"request_parse_ms,omitempty"`
	PreRoutingMs         float64                         `json:"pre_routing_ms,omitempty"`
	SignalInputMs        float64                         `json:"signal_input_ms,omitempty"`
	PromptCompressionMs  float64                         `json:"prompt_compression_ms,omitempty"`
	SignalEvaluationMs   float64                         `json:"signal_evaluation_ms,omitempty"`
	DecisionEvaluationMs float64                         `json:"decision_evaluation_ms,omitempty"`
	ModelSelectionMs     float64                         `json:"model_selection_ms,omitempty"`
	Signals              *decisionOnlySignalTimingsDebug `json:"signals,omitempty"`
}

type decisionOnlySignalTimingsDebug struct {
	KeywordMs      float64 `json:"keyword_ms,omitempty"`
	EmbeddingMs    float64 `json:"embedding_ms,omitempty"`
	DomainMs       float64 `json:"domain_ms,omitempty"`
	FactCheckMs    float64 `json:"fact_check_ms,omitempty"`
	UserFeedbackMs float64 `json:"user_feedback_ms,omitempty"`
	ReaskMs        float64 `json:"reask_ms,omitempty"`
	PreferenceMs   float64 `json:"preference_ms,omitempty"`
	LanguageMs     float64 `json:"language_ms,omitempty"`
	ContextMs      float64 `json:"context_ms,omitempty"`
	StructureMs    float64 `json:"structure_ms,omitempty"`
	ComplexityMs   float64 `json:"complexity_ms,omitempty"`
	ModalityMs     float64 `json:"modality_ms,omitempty"`
	AuthzMs        float64 `json:"authz_ms,omitempty"`
	JailbreakMs    float64 `json:"jailbreak_ms,omitempty"`
	PIIMs          float64 `json:"pii_ms,omitempty"`
	KBMs           float64 `json:"kb_ms,omitempty"`
	ConversationMs float64 `json:"conversation_ms,omitempty"`
	EventMs        float64 `json:"event_ms,omitempty"`
}

type decisionOnlyDecisionDebug struct {
	Name            string                          `json:"name,omitempty"`
	Category        string                          `json:"category,omitempty"`
	Confidence      *float64                        `json:"confidence,omitempty"`
	MatchedRules    []string                        `json:"matched_rules"`
	MatchedKeywords []string                        `json:"matched_keywords"`
	Tier            int                             `json:"tier,omitempty"`
	Priority        int                             `json:"priority,omitempty"`
	Algorithm       string                          `json:"algorithm,omitempty"`
	CandidateModels []decisionOnlyCandidateModelRef `json:"candidate_models"`
}

type decisionOnlyCandidateModelRef struct {
	Model        string  `json:"model"`
	LoRAName     string  `json:"lora_name,omitempty"`
	Weight       float64 `json:"weight,omitempty"`
	UseReasoning *bool   `json:"use_reasoning,omitempty"`
}

type decisionOnlyMatchedSignalsDebug struct {
	Keyword      []string `json:"keyword"`
	KeywordTerms []string `json:"keyword_terms"`
	Embedding    []string `json:"embedding"`
	Domain       []string `json:"domain"`
	FactCheck    []string `json:"fact_check"`
	UserFeedback []string `json:"user_feedback"`
	Reask        []string `json:"reask"`
	Preference   []string `json:"preference"`
	Language     []string `json:"language"`
	Context      []string `json:"context"`
	Structure    []string `json:"structure"`
	Complexity   []string `json:"complexity"`
	Modality     []string `json:"modality"`
	Authz        []string `json:"authz"`
	Jailbreak    []string `json:"jailbreak"`
	PII          []string `json:"pii"`
	KB           []string `json:"kb"`
	Conversation []string `json:"conversation"`
	Event        []string `json:"event"`
	Projection   []string `json:"projection"`
}

type decisionOnlyContextDebug struct {
	ContextTokens          int    `json:"context_tokens,omitempty"`
	ContextTextBytes       int    `json:"context_text_bytes,omitempty"`
	UserMessageCount       int    `json:"user_message_count,omitempty"`
	AssistantMessageCount  int    `json:"assistant_message_count,omitempty"`
	SystemMessageCount     int    `json:"system_message_count,omitempty"`
	ToolMessageCount       int    `json:"tool_message_count,omitempty"`
	ToolDefinitionCount    int    `json:"tool_definition_count,omitempty"`
	AssistantToolCallCount int    `json:"assistant_tool_call_count,omitempty"`
	ToolResultCount        int    `json:"tool_result_count,omitempty"`
	LastMessageRole        string `json:"last_message_role,omitempty"`
	HasDeveloperMessage    bool   `json:"has_developer_message,omitempty"`
}

type requestDecisionTimingInfo struct {
	RequestParseMs       float64
	PreRoutingMs         float64
	SignalInputMs        float64
	PromptCompressionMs  float64
	SignalEvaluationMs   float64
	DecisionEvaluationMs float64
	ModelSelectionMs     float64
}
