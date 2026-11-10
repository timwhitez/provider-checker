package checker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Local budgets for small capability probes and metadata-rich model lists,
// not provider API limits. The model-list fixture covers 1000 entries.
const (
	probeJSONLimit     = 1 << 20
	modelListJSONLimit = 8 << 20
)

var errJSONReadLimit = errors.New("local JSON read limit exceeded")

// decodeJSON bounds decoder reads after HTTP decompression, including at most
// one extra byte to detect overflow. It neither drains nor closes the body,
// and does not wait for EOF after a complete value. Decoder read-ahead counts
// toward the budget; a local truncation must never establish capability PASS.
func decodeJSON(r io.Reader, v any, limit int64) error {
	limited := &io.LimitedReader{R: r, N: limit + 1}
	err := json.NewDecoder(limited).Decode(v)
	if limited.N == 0 {
		return fmt.Errorf("%w (%d bytes)", errJSONReadLimit, limit)
	}
	return err
}
