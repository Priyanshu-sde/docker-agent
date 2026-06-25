// Package plan provides a toolset that lets two or more agents collaborate on
// plans stored in a global shared folder under the docker-agent data
// directory. Plans are addressed by name, so any agent that loads this toolset
// can write, read, list, and delete the same shared plans, and they persist
// across sessions.
//
// Concurrency: all agents in a single process share one ToolSet instance (see
// CreateToolSet), so their reads and writes are serialized by a single mutex.
// On-disk writes are atomic (write-to-temp + rename), so a reader — including
// a separate docker-agent process — never observes a partially written plan.
// Two distinct processes writing the *same* plan at the very same instant can
// still race on the read-modify-write revision bump (last writer wins); this
// is acceptable for the intended in-process multi-agent collaboration.
package plan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker-agent/pkg/atomicfile"
	"github.com/docker/docker-agent/pkg/paths"
	"github.com/docker/docker-agent/pkg/tools"
)

const (
	ToolNameWritePlan  = "write_plan"
	ToolNameReadPlan   = "read_plan"
	ToolNameListPlans  = "list_plans"
	ToolNameDeletePlan = "delete_plan"
)

// namePattern defines the accepted plan-name format: a lowercase slug made of
// alphanumerics, '-' and '_'. Names are validated against it rather than being
// silently rewritten, so two different inputs can never collapse onto the same
// file (which would let one plan clobber another) and no input can escape the
// plans directory via path separators or "..".
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Plan is a shared document collaborated on by the agents.
type Plan struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Content string `json:"content"`
	// Author is a free-form label identifying who last wrote the plan
	// (typically the agent name). It helps collaborators see who made the
	// most recent change.
	Author    string `json:"author,omitempty"`
	Revision  int    `json:"revision"`
	UpdatedAt string `json:"updatedAt"`
}

// Summary is a lightweight view of a plan returned by list_plans.
type Summary struct {
	Name      string `json:"name"`
	Title     string `json:"title,omitempty"`
	Author    string `json:"author,omitempty"`
	Revision  int    `json:"revision"`
	UpdatedAt string `json:"updatedAt"`
}

// ListResult is the output of list_plans. Warnings lists plan files that could
// not be read or decoded, so a caller can tell "no plans exist" apart from
// "some plans failed to load" — important because an agent that mistakes a
// temporarily unreadable plan for a missing one could recreate and clobber it.
type ListResult struct {
	Plans    []Summary `json:"plans"`
	Warnings []string  `json:"warnings,omitempty"`
}

type ToolSet struct {
	mu  sync.Mutex
	dir string
}

var (
	_ tools.ToolSet      = (*ToolSet)(nil)
	_ tools.Instructable = (*ToolSet)(nil)
	_ tools.Describer    = (*ToolSet)(nil)
)

// sharedToolSet returns the one ToolSet shared by every agent in this process,
// built once on first use. Sharing a single instance means all collaborating
// agents serialize their plan operations on the same mutex.
//
// Building the struct cannot fail, so the directory is *not* created here:
// save() runs os.MkdirAll on every write, which means a directory that is
// momentarily unavailable at startup (e.g. a not-yet-mounted parent) is
// recovered from automatically instead of being permanently memoized as an
// error.
var sharedToolSet = sync.OnceValue(func() *ToolSet {
	return New(DefaultDir())
})

// CreateToolSet is used by the tools registry. It returns a process-wide
// singleton so that all agents collaborating in the same process share one
// lock over the global plans folder.
func CreateToolSet() (tools.ToolSet, error) {
	return sharedToolSet(), nil
}

// DefaultDir is the global shared folder where plans are stored, under the
// docker-agent data directory.
func DefaultDir() string {
	return filepath.Join(paths.GetDataDir(), "plans")
}

func New(dir string) *ToolSet {
	return &ToolSet{dir: dir}
}

func (t *ToolSet) Describe() string {
	return "plan(dir=" + t.dir + ")"
}

func (t *ToolSet) Instructions() string {
	return `## Plan Tools

Collaborate on shared, named plans with other agents. Plans are stored in a
global shared folder, so every agent using this toolset sees the same plans.

- Use list_plans to discover existing plans.
- Use read_plan to inspect a plan before acting on or changing it.
- Use write_plan to create or update a plan by name. Writing replaces the whole
  document, so read it first and preserve any content you want to keep. Set the
  title and author (your agent name) so collaborators can see who made the
  latest change. Each write bumps the revision number.
- Use delete_plan to remove a plan once it is no longer needed.

Plan names must be lowercase and may contain only letters, digits, '-' and '_'
(for example "release-2025" or "db_migration").`
}

// planPath validates name and returns the absolute path of its plan file. The
// name is rejected (rather than rewritten) when it does not match namePattern,
// which guarantees a one-to-one mapping between names and files and prevents
// path traversal.
func (t *ToolSet) planPath(name string) (string, error) {
	if !namePattern.MatchString(name) {
		return "", fmt.Errorf("invalid plan name %q: use only lowercase letters, digits, '-' and '_', starting with a letter or digit", name)
	}
	return filepath.Join(t.dir, name+".json"), nil
}

// load reads and decodes the plan at path. It distinguishes a missing plan
// (false, nil) from a real failure such as a permission error or corrupt JSON
// (false, err), so callers can report the latter instead of masking it as
// "not found".
func (t *ToolSet) load(path string) (Plan, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Plan{}, false, nil
	}
	if err != nil {
		return Plan{}, false, fmt.Errorf("reading plan: %w", err)
	}
	var p Plan
	if err := json.Unmarshal(data, &p); err != nil {
		return Plan{}, false, fmt.Errorf("plan file %s is corrupt: %w", filepath.Base(path), err)
	}
	return p, true, nil
}

func (t *ToolSet) save(path string, p Plan) error {
	if err := os.MkdirAll(t.dir, 0o700); err != nil {
		return fmt.Errorf("creating plans directory: %w", err)
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling plan: %w", err)
	}
	// Atomic write (temp file + rename): readers in other agents or processes
	// see either the old or the new content, never a partial file, and an
	// existing symlink at path is replaced rather than followed.
	if err := atomicfile.Write(path, bytes.NewReader(data), 0o600); err != nil {
		return fmt.Errorf("writing plan: %w", err)
	}
	return nil
}

type WritePlanArgs struct {
	Name    string `json:"name" jsonschema:"The plan name. Lowercase letters, digits, '-' and '_' only (e.g. 'release', 'db-migration')."`
	Content string `json:"content" jsonschema:"The full plan content (markdown). Replaces the existing plan."`
	Title   string `json:"title,omitempty" jsonschema:"Optional human-readable title. Preserved from the previous revision when omitted."`
	Author  string `json:"author,omitempty" jsonschema:"Optional label identifying who is writing the plan (typically the agent name). Preserved from the previous revision when omitted."`
}

func (t *ToolSet) writePlan(_ context.Context, params WritePlanArgs) (*tools.ToolCallResult, error) {
	if params.Content == "" {
		return tools.ResultError("content must not be empty"), nil
	}
	path, err := t.planPath(params.Name)
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	plan, _, err := t.load(path)
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	plan.Name = params.Name
	plan.Content = params.Content
	// Title and author are preserved across revisions when omitted, so an
	// agent updating only the content does not wipe the collaboration metadata
	// set by a previous writer.
	if params.Title != "" {
		plan.Title = params.Title
	}
	if params.Author != "" {
		plan.Author = params.Author
	}
	plan.Revision++
	plan.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	if err := t.save(path, plan); err != nil {
		return tools.ResultError(err.Error()), nil
	}

	return tools.ResultJSON(plan), nil
}

type ReadPlanArgs struct {
	Name string `json:"name" jsonschema:"The name of the plan to read."`
}

func (t *ToolSet) readPlan(_ context.Context, params ReadPlanArgs) (*tools.ToolCallResult, error) {
	path, err := t.planPath(params.Name)
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	plan, ok, err := t.load(path)
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	if !ok {
		return tools.ResultError(fmt.Sprintf("plan %q not found", params.Name)), nil
	}

	return tools.ResultJSON(plan), nil
}

func (t *ToolSet) listPlans(_ context.Context, _ tools.ToolCall) (*tools.ToolCallResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	entries, err := os.ReadDir(t.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tools.ResultJSON(ListResult{Plans: []Summary{}}), nil
		}
		return tools.ResultError(err.Error()), nil
	}

	result := ListResult{Plans: []Summary{}}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		// The filename is the authoritative key: read_plan and delete_plan
		// resolve a name to <name>.json, so the listed name must match the
		// filename, not the (possibly drifted) name field inside the JSON.
		name := strings.TrimSuffix(entry.Name(), ".json")
		plan, ok, err := t.load(filepath.Join(t.dir, entry.Name()))
		if err != nil || !ok {
			// Don't abort the whole listing on one bad file, but surface it as
			// a warning so the caller doesn't mistake an unreadable plan for a
			// missing one.
			msg := "unreadable"
			if err != nil {
				msg = err.Error()
			}
			result.Warnings = append(result.Warnings, fmt.Sprintf("skipped %q: %s", name, msg))
			continue
		}
		result.Plans = append(result.Plans, Summary{
			Name:      name,
			Title:     plan.Title,
			Author:    plan.Author,
			Revision:  plan.Revision,
			UpdatedAt: plan.UpdatedAt,
		})
	}

	sort.Slice(result.Plans, func(i, j int) bool {
		return result.Plans[i].Name < result.Plans[j].Name
	})

	return tools.ResultJSON(result), nil
}

type DeletePlanArgs struct {
	Name string `json:"name" jsonschema:"The name of the plan to delete."`
}

func (t *ToolSet) deletePlan(_ context.Context, params DeletePlanArgs) (*tools.ToolCallResult, error) {
	path, err := t.planPath(params.Name)
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Remove the file directly and treat a missing file as "not found". We
	// don't pre-load the plan: a corrupt plan should still be deletable.
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tools.ResultError(fmt.Sprintf("plan %q not found", params.Name)), nil
		}
		return tools.ResultError(err.Error()), nil
	}

	return tools.ResultJSON(map[string]string{"deleted": params.Name}), nil
}

func (t *ToolSet) Tools(context.Context) ([]tools.Tool, error) {
	return []tools.Tool{
		{
			Name:         ToolNameWritePlan,
			Category:     "plan",
			Description:  "Create or update a shared plan by name. Replaces the entire plan content, so read it first to preserve anything you want to keep. Each write bumps the revision number.",
			Parameters:   tools.MustSchemaFor[WritePlanArgs](),
			OutputSchema: tools.MustSchemaFor[Plan](),
			Handler:      tools.NewHandler(t.writePlan),
			Annotations: tools.ToolAnnotations{
				Title: "Write Plan",
			},
		},
		{
			Name:         ToolNameReadPlan,
			Category:     "plan",
			Description:  "Read a shared plan by name, including its title, content, author, and revision number.",
			Parameters:   tools.MustSchemaFor[ReadPlanArgs](),
			OutputSchema: tools.MustSchemaFor[Plan](),
			Handler:      tools.NewHandler(t.readPlan),
			Annotations: tools.ToolAnnotations{
				Title:        "Read Plan",
				ReadOnlyHint: true,
			},
		},
		{
			Name:         ToolNameListPlans,
			Category:     "plan",
			Description:  "List all shared plans with their name, title, author, and revision.",
			OutputSchema: tools.MustSchemaFor[ListResult](),
			Handler:      t.listPlans,
			Annotations: tools.ToolAnnotations{
				Title:        "List Plans",
				ReadOnlyHint: true,
			},
		},
		{
			Name:        ToolNameDeletePlan,
			Category:    "plan",
			Description: "Delete a shared plan by name.",
			Parameters:  tools.MustSchemaFor[DeletePlanArgs](),
			Handler:     tools.NewHandler(t.deletePlan),
			Annotations: tools.ToolAnnotations{
				Title:           "Delete Plan",
				DestructiveHint: new(true),
			},
		},
	}, nil
}
