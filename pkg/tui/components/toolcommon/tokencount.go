package toolcommon

import (
	"fmt"
	"strconv"
)

// FormatTokenCount formats a token count with K/M suffixes for readability
// (e.g. 8200 → "8.2K", 1500000 → "1.5M"). Values below 1000 are rendered
// verbatim. This is the canonical implementation shared by the sidebar and
// the cost/model dialogs.
func FormatTokenCount(count int64) string {
	switch {
	case count >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(count)/1_000_000)
	case count >= 1_000:
		return fmt.Sprintf("%.1fK", float64(count)/1_000)
	default:
		return strconv.FormatInt(count, 10)
	}
}

// FormatCostUSD formats a compact USD amount for status surfaces.
func FormatCostUSD(cost float64) string {
	return fmt.Sprintf("$%.2f", cost)
}
