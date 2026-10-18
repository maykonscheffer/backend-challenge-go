package store

import "encoding/json"

// Keep code for compatibility with existing persisted results; failureCode is the
// explicit contract field for new clients. Both carry the same stable value.
func (r Result) MarshalJSON() ([]byte, error) {
	type plain Result
	return json.Marshal(struct {
		plain
		FailureCode string `json:"failureCode,omitempty"`
	}{plain(r), r.Code})
}
