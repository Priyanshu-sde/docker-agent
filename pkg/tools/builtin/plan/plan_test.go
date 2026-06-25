package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/tools"
)

func newTestPlanTool(t *testing.T) *ToolSet {
	t.Helper()
	return New(t.TempDir())
}

func TestPlanTool_DisplayNames(t *testing.T) {
	tool := newTestPlanTool(t)

	all, err := tool.Tools(t.Context())
	require.NoError(t, err)

	for _, tl := range all {
		assert.NotEmpty(t, tl.DisplayName())
		assert.NotEqual(t, tl.Name, tl.DisplayName())
	}
}

func TestPlanTool_Instructions(t *testing.T) {
	tool := newTestPlanTool(t)
	assert.NotEmpty(t, tool.Instructions())
}

func TestPlanTool_Describe(t *testing.T) {
	tool := newTestPlanTool(t)
	assert.Contains(t, tool.Describe(), "plan(dir=")
}

func TestPlanTool_Write(t *testing.T) {
	tool := newTestPlanTool(t)

	result, err := tool.writePlan(t.Context(), WritePlanArgs{
		Name:    "release",
		Content: "Step 1: do the thing",
		Title:   "Release plan",
		Author:  "planner",
	})
	require.NoError(t, err)
	assert.False(t, result.IsError)

	var plan Plan
	require.NoError(t, json.Unmarshal([]byte(result.Output), &plan))
	assert.Equal(t, "release", plan.Name)
	assert.Equal(t, "Release plan", plan.Title)
	assert.Equal(t, "Step 1: do the thing", plan.Content)
	assert.Equal(t, "planner", plan.Author)
	assert.Equal(t, 1, plan.Revision)
	assert.NotEmpty(t, plan.UpdatedAt)
}

func TestPlanTool_WriteEmptyContent(t *testing.T) {
	tool := newTestPlanTool(t)

	result, err := tool.writePlan(t.Context(), WritePlanArgs{Name: "p", Content: ""})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Output, "content must not be empty")
}

func TestPlanTool_InvalidNames(t *testing.T) {
	tool := newTestPlanTool(t)

	for _, name := range []string{"", "///", "Has Space", "UPPER", "../escape", "a/b", "-leading", "with.dot"} {
		result, err := tool.writePlan(t.Context(), WritePlanArgs{Name: name, Content: "x"})
		require.NoError(t, err)
		assert.True(t, result.IsError, "name %q should be rejected", name)
		assert.Contains(t, result.Output, "invalid plan name")
	}
}

func TestPlanTool_ValidNames(t *testing.T) {
	tool := newTestPlanTool(t)

	for _, name := range []string{"release", "release-2025", "db_migration", "a", "1plan"} {
		result, err := tool.writePlan(t.Context(), WritePlanArgs{Name: name, Content: "x"})
		require.NoError(t, err)
		assert.False(t, result.IsError, "name %q should be accepted: %s", name, result.Output)
	}
}

func TestPlanTool_NoSilentCollision(t *testing.T) {
	tool := newTestPlanTool(t)

	// "a-b" is valid; "a/b" and "a b" are rejected outright rather than
	// being silently mapped onto "a-b" and clobbering it.
	_, err := tool.writePlan(t.Context(), WritePlanArgs{Name: "a-b", Content: "original"})
	require.NoError(t, err)

	for _, colliding := range []string{"a/b", "a b", "a!b"} {
		result, err := tool.writePlan(t.Context(), WritePlanArgs{Name: colliding, Content: "evil"})
		require.NoError(t, err)
		assert.True(t, result.IsError, "name %q must not be accepted", colliding)
	}

	// The original plan is untouched.
	result, err := tool.readPlan(t.Context(), ReadPlanArgs{Name: "a-b"})
	require.NoError(t, err)
	var plan Plan
	require.NoError(t, json.Unmarshal([]byte(result.Output), &plan))
	assert.Equal(t, "original", plan.Content)
}

func TestPlanTool_ReadNotFound(t *testing.T) {
	tool := newTestPlanTool(t)

	result, err := tool.readPlan(t.Context(), ReadPlanArgs{Name: "missing"})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Output, "not found")
}

func TestPlanTool_ReadCorruptReportsError(t *testing.T) {
	tool := newTestPlanTool(t)

	// Write a corrupt plan file directly.
	require.NoError(t, os.WriteFile(filepath.Join(tool.dir, "broken.json"), []byte("{not json"), 0o600))

	result, err := tool.readPlan(t.Context(), ReadPlanArgs{Name: "broken"})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Output, "corrupt")
	assert.NotContains(t, result.Output, "not found")
}

func TestPlanTool_WriteThenRead(t *testing.T) {
	tool := newTestPlanTool(t)

	_, err := tool.writePlan(t.Context(), WritePlanArgs{Name: "migration", Content: "the plan", Title: "T"})
	require.NoError(t, err)

	result, err := tool.readPlan(t.Context(), ReadPlanArgs{Name: "migration"})
	require.NoError(t, err)
	assert.False(t, result.IsError)

	var plan Plan
	require.NoError(t, json.Unmarshal([]byte(result.Output), &plan))
	assert.Equal(t, "the plan", plan.Content)
	assert.Equal(t, "T", plan.Title)
}

func TestPlanTool_RevisionIncrementsAndMetadataPreserved(t *testing.T) {
	tool := newTestPlanTool(t)

	_, err := tool.writePlan(t.Context(), WritePlanArgs{Name: "p", Content: "v1", Title: "Original", Author: "alice"})
	require.NoError(t, err)

	// Second write omits the title and author; both should be preserved.
	result, err := tool.writePlan(t.Context(), WritePlanArgs{Name: "p", Content: "v2"})
	require.NoError(t, err)

	var plan Plan
	require.NoError(t, json.Unmarshal([]byte(result.Output), &plan))
	assert.Equal(t, "v2", plan.Content)
	assert.Equal(t, "Original", plan.Title)
	assert.Equal(t, "alice", plan.Author)
	assert.Equal(t, 2, plan.Revision)
}

func TestPlanTool_AuthorCanBeUpdated(t *testing.T) {
	tool := newTestPlanTool(t)

	_, err := tool.writePlan(t.Context(), WritePlanArgs{Name: "p", Content: "v1", Author: "alice"})
	require.NoError(t, err)

	result, err := tool.writePlan(t.Context(), WritePlanArgs{Name: "p", Content: "v2", Author: "bob"})
	require.NoError(t, err)

	var plan Plan
	require.NoError(t, json.Unmarshal([]byte(result.Output), &plan))
	assert.Equal(t, "bob", plan.Author)
}

func TestPlanTool_ListPlans(t *testing.T) {
	tool := newTestPlanTool(t)

	_, err := tool.writePlan(t.Context(), WritePlanArgs{Name: "beta", Content: "b", Author: "x"})
	require.NoError(t, err)
	_, err = tool.writePlan(t.Context(), WritePlanArgs{Name: "alpha", Content: "a", Author: "y"})
	require.NoError(t, err)

	result, err := tool.listPlans(t.Context(), tools.ToolCall{})
	require.NoError(t, err)
	assert.False(t, result.IsError)

	var list ListResult
	require.NoError(t, json.Unmarshal([]byte(result.Output), &list))
	require.Len(t, list.Plans, 2)
	// Sorted by name.
	assert.Equal(t, "alpha", list.Plans[0].Name)
	assert.Equal(t, "beta", list.Plans[1].Name)
	assert.Empty(t, list.Warnings)
}

func TestPlanTool_ListEmpty(t *testing.T) {
	tool := newTestPlanTool(t)

	result, err := tool.listPlans(t.Context(), tools.ToolCall{})
	require.NoError(t, err)
	assert.False(t, result.IsError)

	var list ListResult
	require.NoError(t, json.Unmarshal([]byte(result.Output), &list))
	assert.Empty(t, list.Plans)
	assert.Empty(t, list.Warnings)
}

func TestPlanTool_ListSkipsCorrupt(t *testing.T) {
	tool := newTestPlanTool(t)

	_, err := tool.writePlan(t.Context(), WritePlanArgs{Name: "good", Content: "ok"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(tool.dir, "bad.json"), []byte("{nope"), 0o600))

	result, err := tool.listPlans(t.Context(), tools.ToolCall{})
	require.NoError(t, err)
	assert.False(t, result.IsError)

	var list ListResult
	require.NoError(t, json.Unmarshal([]byte(result.Output), &list))
	require.Len(t, list.Plans, 1)
	assert.Equal(t, "good", list.Plans[0].Name)
	// The corrupt file is surfaced as a warning rather than silently dropped.
	require.Len(t, list.Warnings, 1)
	assert.Contains(t, list.Warnings[0], "bad")
}

func TestPlanTool_ListNameFromFilename(t *testing.T) {
	tool := newTestPlanTool(t)

	// A plan file whose stored name field disagrees with its filename. The
	// filename is authoritative because read_plan/delete_plan key off it.
	drifted := Plan{Name: "wrong-name", Content: "x", Revision: 1}
	data, err := json.Marshal(drifted)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(tool.dir, "real-name.json"), data, 0o600))

	result, err := tool.listPlans(t.Context(), tools.ToolCall{})
	require.NoError(t, err)
	assert.False(t, result.IsError)

	var list ListResult
	require.NoError(t, json.Unmarshal([]byte(result.Output), &list))
	require.Len(t, list.Plans, 1)
	// The name returned matches the filename, so a follow-up read_plan works.
	assert.Equal(t, "real-name", list.Plans[0].Name)

	read, err := tool.readPlan(t.Context(), ReadPlanArgs{Name: list.Plans[0].Name})
	require.NoError(t, err)
	assert.False(t, read.IsError)
}

func TestPlanTool_DeletePlan(t *testing.T) {
	tool := newTestPlanTool(t)

	_, err := tool.writePlan(t.Context(), WritePlanArgs{Name: "temp", Content: "x"})
	require.NoError(t, err)

	result, err := tool.deletePlan(t.Context(), DeletePlanArgs{Name: "temp"})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Output, "temp")

	// Verify it's gone.
	readResult, err := tool.readPlan(t.Context(), ReadPlanArgs{Name: "temp"})
	require.NoError(t, err)
	assert.True(t, readResult.IsError)
}

func TestPlanTool_DeleteCorruptSucceeds(t *testing.T) {
	tool := newTestPlanTool(t)

	require.NoError(t, os.WriteFile(filepath.Join(tool.dir, "broken.json"), []byte("{nope"), 0o600))

	result, err := tool.deletePlan(t.Context(), DeletePlanArgs{Name: "broken"})
	require.NoError(t, err)
	assert.False(t, result.IsError, "a corrupt plan should still be deletable")
}

func TestPlanTool_DeleteNotFound(t *testing.T) {
	tool := newTestPlanTool(t)

	result, err := tool.deletePlan(t.Context(), DeletePlanArgs{Name: "nope"})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Output, "not found")
}

func TestPlanTool_SharedAcrossInstances(t *testing.T) {
	dir := t.TempDir()

	// One agent writes the plan.
	writer := New(dir)
	_, err := writer.writePlan(t.Context(), WritePlanArgs{
		Name:    "collab",
		Content: "collaborative plan",
		Author:  "agent-a",
	})
	require.NoError(t, err)

	// Another agent, sharing the same folder, reads it.
	reader := New(dir)
	result, err := reader.readPlan(t.Context(), ReadPlanArgs{Name: "collab"})
	require.NoError(t, err)
	assert.False(t, result.IsError)

	var plan Plan
	require.NoError(t, json.Unmarshal([]byte(result.Output), &plan))
	assert.Equal(t, "collaborative plan", plan.Content)
	assert.Equal(t, "agent-a", plan.Author)
}

func TestPlanTool_ParametersAreObjects(t *testing.T) {
	tool := newTestPlanTool(t)

	allTools, err := tool.Tools(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, allTools)

	for _, tl := range allTools {
		if tl.Parameters == nil {
			continue
		}
		m, err := tools.SchemaToMap(tl.Parameters)
		require.NoError(t, err)
		assert.Equal(t, "object", m["type"])
	}
}
