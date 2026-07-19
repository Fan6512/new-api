package dto

type UnaBillingLogCursor struct {
	CreatedAt int64  `json:"created_at"`
	ID        string `json:"id"`
}

type UnaBillingLogRow struct {
	ID                 string  `json:"id"`
	CreatedAt          int64   `json:"created_at"`
	TokenID            string  `json:"token_id"`
	ModelName          string  `json:"model_name"`
	ChannelID          *uint64 `json:"channel_id"`
	ChannelName        string  `json:"channel_name"`
	RequestPath        string  `json:"request_path"`
	IsStream           bool    `json:"is_stream"`
	PromptTokens       *int64  `json:"prompt_tokens"`
	CachedPromptTokens *int64  `json:"cached_prompt_tokens"`
	CompletionTokens   *int64  `json:"completion_tokens"`
	ImageCount         *int64  `json:"image_count"`
	Quota              string  `json:"quota"`
	LatencyMS          int64   `json:"latency_ms"`
	Status             string  `json:"status"`
	ErrorCode          string  `json:"error_code"`
}

type UnaBillingLogResponse struct {
	ContractVersion string                `json:"contract_version"`
	Data            []UnaBillingLogRow    `json:"data"`
	NextCursor      *UnaBillingLogCursor  `json:"next_cursor"`
	HasMore         bool                  `json:"has_more"`
}
