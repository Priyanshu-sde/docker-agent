package toolcommon

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatCostUSD(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "$0.00", FormatCostUSD(0))
	assert.Equal(t, "$0.05", FormatCostUSD(0.05))
	assert.Equal(t, "$1.23", FormatCostUSD(1.234))
	assert.Equal(t, "$-0.05", FormatCostUSD(-0.05))
}
