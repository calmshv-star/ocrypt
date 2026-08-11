package domain

type Asset struct {
	ID             string `json:"id"`
	ChainID        string `json:"chain_id"`
	Symbol         string `json:"symbol"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Contract       string `json:"contract,omitempty"`
	Decimals       uint8  `json:"decimals"`
	Status         string `json:"status"`
	MinimumDeposit string `json:"minimum_deposit_atomic"`
}
