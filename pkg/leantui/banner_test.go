package leantui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommitWelcomePadsBanner(t *testing.T) {
	t.Parallel()
	m := &model{transcript: newTranscript()}
	m.commitWelcome()

	require.Len(t, m.transcript.blocks, 1)
	lines := m.transcript.blocks[0].lines(80)
	require.GreaterOrEqual(t, len(lines), bannerTopPadding+len(bannerLines))

	for i := range bannerTopPadding {
		assert.Empty(t, ansi.Strip(lines[i]))
	}

	leftPad := strings.Repeat(" ", bannerLeftPadding)
	firstBannerLine := ansi.Strip(lines[bannerTopPadding])
	assert.True(t, strings.HasPrefix(firstBannerLine, leftPad))
	assert.Equal(t, leftPad+bannerLines[0], firstBannerLine)

	helpLine := ansi.Strip(lines[len(lines)-1])
	assert.True(t, strings.HasPrefix(helpLine, leftPad))
}
