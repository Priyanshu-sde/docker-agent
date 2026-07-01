// Package plantool renders the single-plan write/status plan tool calls
// (write_plan, set_plan_status, get_plan_status, update_plan_from_file,
// export_plan_to_file). It surfaces the plan's status (and title) right next to
// the tool call, so a reader can see a plan's lifecycle at a glance instead of
// hunting for it in the JSON body.
//
// read_plan, list_plans and delete_plan intentionally keep the default
// renderer: read_plan is meant to show the full plan body (which the default
// renderer prints, with the status still present in the JSON), list_plans
// returns many plans, and delete_plan has no status to surface.
package plantool

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/docker/docker-agent/pkg/tui/components/toolcommon"
	"github.com/docker/docker-agent/pkg/tui/core/layout"
	"github.com/docker/docker-agent/pkg/tui/service"
	"github.com/docker/docker-agent/pkg/tui/types"
)

// New builds the plan tool view: the plan name from the call arguments, plus a
// compact title/status/revision summary from the result.
func New(msg *types.Message, sessionState service.SessionStateReader) layout.Model {
	return toolcommon.NewBase(msg, sessionState, toolcommon.SimpleRendererWithResult(
		toolcommon.ExtractField(func(a nameArg) string { return a.Name }),
		extractSummary,
	))
}

type nameArg struct {
	Name string `json:"name"`
}

func extractName(args string) string {
	return toolcommon.ExtractField(func(a nameArg) string { return a.Name })(args)
}

// planResult captures the result fields worth surfacing in the header. Plan,
// StatusView and ExportResult all share this shape, so one decode covers every
// single-plan tool.
type planResult struct {
	Title    string `json:"title"`
	Status   string `json:"status"`
	Revision int    `json:"revision"`
}

func extractSummary(msg *types.Message) string {
	// On failure, surface the backend message verbatim (e.g. a version
	// conflict): that is exactly what the reader needs in order to react.
	if msg.ToolStatus == types.ToolStatusError {
		return msg.Content
	}

	var r planResult
	if err := json.Unmarshal([]byte(msg.Content), &r); err != nil {
		return ""
	}

	var parts []string
	if r.Title != "" {
		parts = append(parts, fmt.Sprintf("%q", r.Title))
	}
	if r.Status != "" {
		parts = append(parts, "["+r.Status+"]")
	}
	if r.Revision > 0 {
		parts = append(parts, fmt.Sprintf("rev %d", r.Revision))
	}
	return strings.Join(parts, " ")
}
