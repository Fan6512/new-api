package dto

type UnaChannelMirrorRequest struct {
	ContractVersion string  `json:"contract_version"`
	MappingID       string  `json:"mapping_id"`
	DesiredVersion  uint64  `json:"desired_version"`
	ChannelID       *uint64 `json:"channel_id"`
	Model           string  `json:"model"`
	ChannelName     string  `json:"channel_name"`
	UpstreamModel   string  `json:"upstream_model"`
	Priority        int64   `json:"priority"`
	Weight          int64   `json:"weight"`
	CostRatio       string  `json:"cost_ratio"`
	Enabled         bool    `json:"enabled"`
}

type UnaChannelMirrorResponse struct {
	ContractVersion string `json:"contract_version"`
	ChannelID       uint64 `json:"channel_id"`
	DesiredVersion  uint64 `json:"desired_version"`
}
