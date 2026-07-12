package dto

type UnaTokenProvisionRequest struct {
	OwnerUserID int    `json:"owner_user_id"`
	Name        string `json:"name"`
	Quota       int    `json:"quota"`
	Status      int    `json:"status"`
}

type UnaTokenProvisionResponse struct {
	ContractVersion string `json:"contract_version"`
	ExternalID      string `json:"external_id"`
	RawKey          string `json:"raw_key"`
	DisplayPrefix   string `json:"display_prefix"`
	Last4           string `json:"last4"`
	Replayed        bool   `json:"replayed"`
}

type UnaTokenDisableResponse struct {
	ContractVersion string `json:"contract_version"`
	ExternalID      string `json:"external_id"`
	Status          string `json:"status"`
}
