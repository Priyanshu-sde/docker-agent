package messages

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/docker/docker-agent/pkg/tui/components/message"
	"github.com/docker/docker-agent/pkg/tui/core/layout"
	"github.com/docker/docker-agent/pkg/tui/service"
)

// TestLongSessionDoesNotRetainPerMessageRenderState streams a long sequence
// of large assistant messages through the same public API the TUI uses
// (AddUserMessage / AddAssistantMessage / AppendToLastMessage), rendering the
// active assistant view between chunks so messageModel.renderCache and
// IncrementalRenderer state are exercised. It asserts two things:
//
//  1. Heap growth stays far below the pre-fix slope. Before the fix every
//     assistant view kept its own renderCache (~70 KB rendered ANSI) and an
//     IncrementalRenderer (cached prefix + rendered output) for a measured
//     ~161 KB of retained state per message. After the fix, per-message render
//     state is released as soon as the message stops being the active
//     streaming view, so the remaining slope is mostly the raw session content.
//
//  2. The structural invariant: after every non-active assistant view has
//     been finalized, only the single actively-streaming view (at most one)
//     should be holding a non-empty renderCache or a live IncrementalRenderer.
//
// Run with -short to skip; the test is deterministic but takes a few seconds.
func TestLongSessionDoesNotRetainPerMessageRenderState(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping leak test in -short mode")
	}

	// `go test` may reset runtime.MemProfileRate when its own -memprofile flag
	// is unset, which would override the value set in init().
	runtime.MemProfileRate = 1

	const (
		warmupMessages  = 200
		measureMessages = 200
		chunkSize       = 256
		messageBytes    = 8192
		viewWidth       = 120
		viewHeight      = 40

		// Per-message heap budget for the retained slope. Pre-fix slope was
		// ~161 KB/msg and did not flatten. The remaining post-fix slope is mostly
		// the original message content and small per-view bookkeeping.
		maxGrowthPerMessageBytes = 30 * 1024
	)

	sessionState := &service.SessionState{}
	m := NewScrollableView(viewWidth, viewHeight, sessionState).(*model)
	m.SetSize(viewWidth, viewHeight)

	body := buildMarkdownBody(messageBytes)

	heapInuse := func() uint64 {
		runtime.GC()
		runtime.GC() // second pass to settle finalizers
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return ms.HeapInuse
	}

	renderLastView := func() {
		if len(m.views) == 0 {
			return
		}
		_ = m.views[len(m.views)-1].View()
	}

	streamMessage := func(i int) {
		m.AddUserMessage(fmt.Sprintf("user message %d", i))
		m.AddAssistantMessage("", "")
		for off := 0; off < len(body); off += chunkSize {
			end := min(off+chunkSize, len(body))
			m.AppendToLastMessage("root", body[off:end])
			if (off/chunkSize)%4 == 0 {
				renderLastView()
			}
		}
		renderLastView()
	}

	// Warmup: stream enough messages to saturate the chat-list LRU.
	for i := range warmupMessages {
		streamMessage(i)
	}
	afterWarmup := heapInuse()

	// Measurement: stream another batch and observe the slope.
	for i := range measureMessages {
		streamMessage(warmupMessages + i)
	}
	afterMeasure := heapInuse()

	growth := max(int64(afterMeasure)-int64(afterWarmup), 0)
	perMessage := growth / measureMessages

	t.Logf("warmup_heap=%d KB measure_heap=%d KB delta=%d KB perMsg=%d KB (budget=%d KB)",
		afterWarmup/1024, afterMeasure/1024, growth/1024, perMessage/1024,
		maxGrowthPerMessageBytes/1024)

	live := countLiveRenderState(t, m.views)
	t.Logf("views=%d live render-state holders=%d (LRU cap=%d, current=%d)",
		len(m.views), live, renderedItemsCacheSize, m.renderedItems.Len())

	if perMessage > maxGrowthPerMessageBytes {
		t.Fatalf("heap growth %d B/message exceeds budget %d B/message — "+
			"per-message render state appears to be retained after finalization",
			perMessage, maxGrowthPerMessageBytes)
	}

	if live > 2 {
		t.Fatalf("expected at most 2 messageModels to hold render-cache state "+
			"(the active streaming view), got %d out of %d views",
			live, len(m.views))
	}
}

// countLiveRenderState walks the view list and reports how many message.Model
// views still hold per-message render state — a populated renderCache or an
// IncrementalRenderer. Uses the exported HasLiveRenderState() so the test
// does not depend on reflection across package boundaries.
func countLiveRenderState(t *testing.T, views []layout.Model) int {
	t.Helper()
	var live int
	for _, v := range views {
		mv, ok := v.(message.Model)
		if !ok {
			continue
		}
		if mv.HasLiveRenderState() {
			live++
		}
	}
	return live
}

// buildMarkdownBody returns a deterministic markdown blob of approximately
// the requested size. The mix of prose paragraphs and a fenced code block
// exercises both stable-boundary detection and code-block caching in the
// IncrementalRenderer (the two structures called out in the leak hypothesis).
func buildMarkdownBody(targetBytes int) string {
	const paragraph = "Lorem ipsum dolor sit amet, consectetur adipiscing elit. " +
		"Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. " +
		"Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris " +
		"nisi ut aliquip ex ea commodo consequat.\n\n"
	const codeBlock = "```go\n" +
		"func example(x int) int {\n" +
		"    // some representative code\n" +
		"    return x * 2\n" +
		"}\n" +
		"```\n\n"

	var b strings.Builder
	b.Grow(targetBytes + len(paragraph))
	for b.Len() < targetBytes {
		b.WriteString(paragraph)
		if b.Len()%3 == 0 {
			b.WriteString(codeBlock)
		}
	}
	return b.String()
}
