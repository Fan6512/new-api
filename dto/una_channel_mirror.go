package dto

import "encoding/json"

type UnaChannelMirrorRequest struct {
	ContractVersion       string          `json:"contract_version"`
	MappingID             string          `json:"mapping_id"`
	DesiredVersion        uint64          `json:"desired_version"`
	ChannelDesiredVersion uint64          `json:"channel_desired_version"`
	ChannelID             *uint64         `json:"channel_id"`
	Model                 string          `json:"model"`
	ChannelName           string          `json:"channel_name"`
	Provider              string          `json:"provider"`
	AdapterType           string          `json:"adapter_type"`
	BaseURL               string          `json:"base_url"`
	Settings              json.RawMessage `json:"settings"`
	Credential            string          `json:"credential"`
	UpstreamModel         string          `json:"upstream_model"`
	Priority              int64           `json:"priority"`
	Weight                int64           `json:"weight"`
	CostRatio             string          `json:"cost_ratio"`
	Enabled               bool            `json:"enabled"`
}

type UnaChannelMirrorResponse struct {
	ContractVersion string `json:"contract_version"`
	ChannelID       uint64 `json:"channel_id"`
	DesiredVersion  uint64 `json:"desired_version"`
}
