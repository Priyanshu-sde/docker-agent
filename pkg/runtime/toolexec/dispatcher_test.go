package toolexec_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/agent"
	"github.com/docker/docker-agent/pkg/chat"
	"github.com/docker/docker-agent/pkg/hooks"
	"github.com/docker/docker-agent/pkg/runtime/toolexec"
	"github.com/docker/docker-agent/pkg/session"
	"github.com/docker/docker-agent/pkg/tools"
)

// captureEmitter records every event the dispatcher emits, in order, so
// tests can make precise assertions about the dispatch flow. A confirm
// channel signals when a confirmation event lands so cancellation tests
// don't need to busy-wait.
type captureEmitter struct {
	calls            []tools.ToolCall
	outputs          []outputRecord
	responses        []responseRecord
	confirmations    []tools.ToolCall
	confirmationMeta []map[string]string
	hookBlocks       []hookBlockRecord
	messages         []*session.Message
	confirmed        chan struct{} // optional; closed after the first confirmation event
}

type outputRecord struct {
	ToolCallID string
	Output     string
}

type responseRecord struct {
	ToolCallID string
	Output     string
	IsError    bool
}

type hookBlockRecord struct {
	ToolCall tools.ToolCall
	Message  string
}

func (e *captureEmitter) EmitToolCall(tc tools.ToolCall, _ tools.Tool, _ string) {
	e.calls = append(e.calls, tc)
}

func (e *captureEmitter) EmitToolCallOutput(toolCallID string, _ tools.Tool, output, _ string) {
	e.outputs = append(e.outputs, outputRecord{ToolCallID: toolCallID, Output: output})
}

func (e *captureEmitter) EmitToolCallResponse(toolCallID string, _ tools.Tool, result *tools.ToolCallResult, output, _ string) {
	e.responses = append(e.responses, responseRecord{
		ToolCallID: toolCallID,
		Output:     output,
		IsError:    result.IsError,
	})
}

func (e *captureEmitter) EmitToolCallConfirmation(tc tools.ToolCall, _ tools.Tool, _ string, metadata map[string]string) {
	e.confirmations = append(e.confirmations, tc)
	e.confirmationMeta = append(e.confirmationMeta, metadata)
	if e.confirmed != nil {
		select {
		case <-e.confirmed:
			// already closed
		default:
			close(e.confirmed)
		}
	}
}

func (e *captureEmitter) EmitHookBlocked(tc tools.ToolCall, _ tools.Tool, message, _ string) {
	e.hookBlocks = append(e.hookBlocks, hookBlockRecord{ToolCall: tc, Message: message})
}

func (e *captureEmitter) EmitMessageAdded(_ string, msg *session.Message, _ string) {
	e.messages = append(e.messages, msg)
}

func newAgent() *agent.Agent {
	return agent.New("test", "test agent")
}

func TestDispatcher_RoutesToToolsetHandler(t *testing.T) {
	a := newAgent()
	sess := session.New()
	sess.ToolsApproved = true // skip approval so we exercise the happy path

	var handlerCalls int
	tool := tools.Tool{
		Name: "echo",
		Handler: func(_ context.Context, tc tools.ToolCall) (*tools.ToolCallResult, error) {
			handlerCalls++
			return tools.ResultSuccess("hello " + tc.Function.Arguments), nil
		},
	}

	d := &toolexec.Dispatcher{
		AgentFor: func(*session.Session) *agent.Agent { return a },
	}
	em := &captureEmitter{}

	d.Process(t.Context(), sess, []tools.ToolCall{{
		ID:       "call_1",
		Function: tools.FunctionCall{Name: "echo", Arguments: `{"x":1}`},
	}}, []tools.Tool{tool}, em)

	assert.Equal(t, 1, handlerCalls)
	require.Len(t, em.responses, 1)
	assert.Equal(t, `hello {"x":1}`, em.responses[0].Output)
	assert.False(t, em.responses[0].IsError)
}

func TestDispatcher_EmitsToolOutputFromHandlerContext(t *testing.T) {
	a := newAgent()
	sess := session.New()
	sess.ToolsApproved = true

	tool := tools.Tool{
		Name: "streamer",
		Handler: func(ctx context.Context, _ tools.ToolCall) (*tools.ToolCallResult, error) {
			tools.EmitOutput(ctx, "first\n")
			tools.EmitOutput(ctx, "second\n")
			return tools.ResultSuccess("done"), nil
		},
	}

	d := &toolexec.Dispatcher{AgentFor: func(*session.Session) *agent.Agent { return a }}
	em := &captureEmitter{}
	d.Process(t.Context(), sess, []tools.ToolCall{{
		ID:       "call_stream",
		Function: tools.FunctionCall{Name: "streamer", Arguments: `{}`},
	}}, []tools.Tool{tool}, em)

	require.Len(t, em.outputs, 2)
	assert.Equal(t, outputRecord{ToolCallID: "call_stream", Output: "first\n"}, em.outputs[0])
	assert.Equal(t, outputRecord{ToolCallID: "call_stream", Output: "second\n"}, em.outputs[1])
	require.Len(t, em.responses, 1)
	assert.Equal(t, "done", em.responses[0].Output)
}

func TestDispatcher_RecordsDocumentToolResult(t *testing.T) {
	a := newAgent()
	sess := session.New()
	sess.ToolsApproved = true

	tool := tools.Tool{
		Name: "report",
		Handler: func(context.Context, tools.ToolCall) (*tools.ToolCallResult, error) {
			return &tools.ToolCallResult{
				Output: "created report",
				Documents: []tools.DocumentContent{{
					Name:     "report.pdf",
					URI:      "file:///report.pdf",
					MimeType: "application/pdf",
					Data:     "UERG",
				}},
			}, nil
		},
	}

	d := &toolexec.Dispatcher{AgentFor: func(*session.Session) *agent.Agent { return a }}
	d.Process(t.Context(), sess, []tools.ToolCall{{
		ID:       "call_report",
		Function: tools.FunctionCall{Name: "report", Arguments: `{}`},
	}}, []tools.Tool{tool}, &captureEmitter{})

	require.Len(t, sess.Messages, 1)
	msg := sess.Messages[0].Message.Message
	require.Len(t, msg.MultiContent, 2)
	assert.Equal(t, chat.MessagePartTypeText, msg.MultiContent[0].Type)
	assert.Equal(t, "created report", msg.MultiContent[0].Text)
	require.NotNil(t, msg.MultiContent[1].Document)
	assert.Equal(t, chat.MessagePartTypeDocument, msg.MultiContent[1].Type)
	assert.Equal(t, "report.pdf", msg.MultiContent[1].Document.Name)
	assert.Equal(t, "application/pdf", msg.MultiContent[1].Document.MimeType)
	assert.Equal(t, []byte("PDF"), msg.MultiContent[1].Document.Source.InlineData)
}

func TestDispatcher_RoutesToRuntimeHandler(t *testing.T) {
	a := newAgent()
	sess := session.New()
	sess.ToolsApproved = true

	var handlerCalls int
	d := &toolexec.Dispatcher{
		AgentFor: func(*session.Session) *agent.Agent { return a },
		Handlers: map[string]toolexec.ToolHandler{
			"transfer_task": func(_ context.Context, _ *session.Session, _ tools.ToolCall) (*tools.ToolCallResult, error) {
				handlerCalls++
				return tools.ResultSuccess("transferred"), nil
			},
		},
	}
	em := &captureEmitter{}

	// Toolset handler must NOT be called when a runtime handler is registered
	// for the same name.
	tool := tools.Tool{
		Name: "transfer_task",
		Handler: func(context.Context, tools.ToolCall) (*tools.ToolCallResult, error) {
			t.Fatal("toolset handler must not be called when runtime handler exists")
			return nil, nil
		},
	}

	d.Process(t.Context(), sess, []tools.ToolCall{{
		ID:       "call_t",
		Function: tools.FunctionCall{Name: "transfer_task", Arguments: "{}"},
	}}, []tools.Tool{tool}, em)

	assert.Equal(t, 1, handlerCalls)
	require.Len(t, em.responses, 1)
	assert.Equal(t, "transferred", em.responses[0].Output)
}

func TestDispatcher_UnknownToolEmitsErrorResponse(t *testing.T) {
	a := newAgent()
	sess := session.New()

	d := &toolexec.Dispatcher{
		AgentFor: func(*session.Session) *agent.Agent { return a },
	}
	em := &captureEmitter{}

	d.Process(t.Context(), sess, []tools.ToolCall{{
		ID:       "ghost",
		Function: tools.FunctionCall{Name: "non_existent", Arguments: "{}"},
	}}, []tools.Tool{}, em)

	require.Len(t, em.responses, 1)
	assert.Equal(t, "ghost", em.responses[0].ToolCallID)
	assert.True(t, em.responses[0].IsError)
	assert.Contains(t, em.responses[0].Output, "not available")
}

func TestDispatcher_UserCancellationStopsBatchAndSynthesizesRemaining(t *testing.T) {
	a := newAgent()
	sess := session.New()

	resume := make(chan toolexec.ResumeRequest, 1)
	d := &toolexec.Dispatcher{
		AgentFor: func(*session.Session) *agent.Agent { return a },
		Resume:   resume,
	}
	em := &captureEmitter{confirmed: make(chan struct{})}

	tool := tools.Tool{
		Name:     "shell",
		Category: "shell",
		Handler:  func(context.Context, tools.ToolCall) (*tools.ToolCallResult, error) { panic("must not run") },
	}

	// Cancel as soon as the dispatcher asks for confirmation on the first
	// call. The remaining two calls in the batch must receive synthetic
	// error responses so the conversation history stays consistent (the
	// Responses API rejects orphaned tool calls).
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		<-em.confirmed
		cancel()
	}()

	calls := []tools.ToolCall{
		{ID: "a", Function: tools.FunctionCall{Name: "shell", Arguments: "{}"}},
		{ID: "b", Function: tools.FunctionCall{Name: "shell", Arguments: "{}"}},
		{ID: "c", Function: tools.FunctionCall{Name: "shell", Arguments: "{}"}},
	}
	d.Process(ctx, sess, calls, []tools.Tool{tool}, em)

	require.Len(t, em.responses, 3, "every call must produce a response")
	for _, r := range em.responses {
		assert.True(t, r.IsError, "every cancelled call must surface as an error response")
	}
	assert.Contains(t, em.responses[0].Output, "canceled by the user")
	assert.Contains(t, em.responses[1].Output, "previous tool call")
	assert.Contains(t, em.responses[2].Output, "previous tool call")
}

func TestDispatcher_ResumeApproveRunsTool(t *testing.T) {
	a := newAgent()
	sess := session.New()

	var ran bool
	tool := tools.Tool{
		Name: "shell",
		Handler: func(context.Context, tools.ToolCall) (*tools.ToolCallResult, error) {
			ran = true
			return tools.ResultSuccess("done"), nil
		},
	}

	resume := make(chan toolexec.ResumeRequest, 1)
	d := &toolexec.Dispatcher{
		AgentFor: func(*session.Session) *agent.Agent { return a },
		Resume:   resume,
	}
	em := &captureEmitter{}

	// Pre-approve via the resume channel before invoking Process.
	resume <- toolexec.ResumeRequest{Type: toolexec.ResumeTypeApprove}

	d.Process(t.Context(), sess, []tools.ToolCall{{
		ID:       "x",
		Function: tools.FunctionCall{Name: "shell", Arguments: "{}"},
	}}, []tools.Tool{tool}, em)

	assert.True(t, ran)
	require.Len(t, em.responses, 1)
	assert.False(t, em.responses[0].IsError)
	assert.Equal(t, "done", em.responses[0].Output)
}

func TestDispatcher_ResumeRejectEmitsErrorResponseWithReason(t *testing.T) {
	a := newAgent()
	sess := session.New()

	tool := tools.Tool{
		Name:    "shell",
		Handler: func(context.Context, tools.ToolCall) (*tools.ToolCallResult, error) { panic("must not run") },
	}

	resume := make(chan toolexec.ResumeRequest, 1)
	d := &toolexec.Dispatcher{
		AgentFor: func(*session.Session) *agent.Agent { return a },
		Resume:   resume,
	}
	em := &captureEmitter{}

	resume <- toolexec.ResumeRequest{Type: toolexec.ResumeTypeReject, Reason: "wrong arguments"}

	d.Process(t.Context(), sess, []tools.ToolCall{{
		ID:       "x",
		Function: tools.FunctionCall{Name: "shell", Arguments: "{}"},
	}}, []tools.Tool{tool}, em)

	require.Len(t, em.responses, 1)
	assert.True(t, em.responses[0].IsError)
	assert.Contains(t, em.responses[0].Output, "user rejected")
	assert.Contains(t, em.responses[0].Output, "wrong arguments")
}

func TestDispatcher_ResumeApproveToolPersistsToSessionPermissions(t *testing.T) {
	a := newAgent()
	sess := session.New()

	var ran bool
	tool := tools.Tool{
		Name: "shell",
		Handler: func(context.Context, tools.ToolCall) (*tools.ToolCallResult, error) {
			ran = true
			return tools.ResultSuccess("ok"), nil
		},
	}

	resume := make(chan toolexec.ResumeRequest, 1)
	d := &toolexec.Dispatcher{
		AgentFor: func(*session.Session) *agent.Agent { return a },
		Resume:   resume,
	}
	em := &captureEmitter{}

	resume <- toolexec.ResumeRequest{Type: toolexec.ResumeTypeApproveTool, ToolName: "shell"}

	d.Process(t.Context(), sess, []tools.ToolCall{{
		ID:       "x",
		Function: tools.FunctionCall{Name: "shell", Arguments: "{}"},
	}}, []tools.Tool{tool}, em)

	assert.True(t, ran)
	require.NotNil(t, sess.Permissions, "approve-tool must seed session permissions")
	assert.Contains(t, sess.Permissions.Allow, "shell")
}

func TestDispatcher_ReadOnlyHintAutoApproves(t *testing.T) {
	a := newAgent()
	sess := session.New() // ToolsApproved=false; no permissions configured

	var ran bool
	tool := tools.Tool{
		Name: "read_file",
		Annotations: tools.ToolAnnotations{
			ReadOnlyHint: true,
		},
		Handler: func(context.Context, tools.ToolCall) (*tools.ToolCallResult, error) {
			ran = true
			return tools.ResultSuccess("contents"), nil
		},
	}

	d := &toolexec.Dispatcher{
		AgentFor: func(*session.Session) *agent.Agent { return a },
	}
	em := &captureEmitter{}

	d.Process(t.Context(), sess, []tools.ToolCall{{
		ID:       "r",
		Function: tools.FunctionCall{Name: "read_file", Arguments: "{}"},
	}}, []tools.Tool{tool}, em)

	assert.True(t, ran)
	assert.Empty(t, em.confirmations, "read-only tool must not prompt the user")
	require.Len(t, em.responses, 1)
	assert.False(t, em.responses[0].IsError)
}

func TestDispatcher_DenyByPermissionsEmitsErrorResponse(t *testing.T) {
	a := newAgent()
	sess := session.New()

	tool := tools.Tool{
		Name:    "shell",
		Handler: func(context.Context, tools.ToolCall) (*tools.ToolCallResult, error) { panic("must not run") },
	}

	d := &toolexec.Dispatcher{
		AgentFor: func(*session.Session) *agent.Agent { return a },
		Permissions: func(*session.Session) []toolexec.NamedChecker {
			return []toolexec.NamedChecker{
				{Checker: newDenyChecker("shell"), Source: "test policy"},
			}
		},
	}
	em := &captureEmitter{}

	d.Process(t.Context(), sess, []tools.ToolCall{{
		ID:       "x",
		Function: tools.FunctionCall{Name: "shell", Arguments: "{}"},
	}}, []tools.Tool{tool}, em)

	require.Len(t, em.responses, 1)
	assert.True(t, em.responses[0].IsError)
	assert.Contains(t, em.responses[0].Output, "denied by test policy")
}

// TestDispatcher_ToolResponseTransformRewritesOutput pins the contract
// of the new tool_response_transform hook: when a configured hook
// returns HookSpecificOutput.UpdatedToolResponse, the dispatcher
// applies it BEFORE event emission, the recorded chat message, and
// the post_tool_use hook input. This is the third leg of the
// redact_secrets feature — unit-tested here at the contract level
// with a stub HookDispatcher so the test doesn't depend on the
// portcullis ruleset shipping a particular set of patterns.
func TestDispatcher_ToolResponseTransformRewritesOutput(t *testing.T) {
	a := newAgent()
	sess := session.New()
	sess.ToolsApproved = true

	original := "raw output with a secret"
	rewritten := "output with [REDACTED]"

	tool := tools.Tool{
		Name:     "leaky",
		Category: "filesystem",
		Handler: func(context.Context, tools.ToolCall) (*tools.ToolCallResult, error) {
			return tools.ResultSuccess(original), nil
		},
	}

	hd := &stubHookDispatcher{
		on: map[hooks.EventType]*hooks.Result{
			hooks.EventToolResponseTransform: {Allowed: true, UpdatedToolResponse: &rewritten},
		},
	}

	d := &toolexec.Dispatcher{
		AgentFor: func(*session.Session) *agent.Agent { return a },
		Hooks:    hd,
	}
	em := &captureEmitter{}

	d.Process(t.Context(), sess, []tools.ToolCall{{
		ID:       "r",
		Function: tools.FunctionCall{Name: "leaky", Arguments: "{}"},
	}}, []tools.Tool{tool}, em)

	require.Len(t, em.responses, 1)
	assert.Equal(t, rewritten, em.responses[0].Output,
		"emitted response must carry the rewritten payload")
	require.Len(t, em.messages, 1)
	assert.Equal(t, rewritten, em.messages[0].Message.Content,
		"recorded chat message must carry the rewritten payload")

	// post_tool_use must see the rewritten payload in its Input —
	// proves the rewrite happens before the post-hook fires, not
	// after.
	require.NotNil(t, hd.lastPostToolInput, "post_tool_use must have been dispatched")
	assert.Equal(t, rewritten, hd.lastPostToolInput.ToolResponse,
		"post_tool_use input must reflect the rewrite")
}

// TestDispatcher_ToolResponseTransformIsNoOpWithoutHooks pins the
// opt-in semantics: with no Hooks dispatcher (or with a hook that
// returns nil for the event), the original output flows through
// untouched and no surprise allocations happen.
func TestDispatcher_ToolResponseTransformIsNoOpWithoutHooks(t *testing.T) {
	a := newAgent()
	sess := session.New()
	sess.ToolsApproved = true

	original := "untouched output"

	tool := tools.Tool{
		Name:     "leaky",
		Category: "filesystem",
		Handler: func(context.Context, tools.ToolCall) (*tools.ToolCallResult, error) {
			return tools.ResultSuccess(original), nil
		},
	}

	d := &toolexec.Dispatcher{
		AgentFor: func(*session.Session) *agent.Agent { return a },
		// Hooks deliberately nil.
	}
	em := &captureEmitter{}

	d.Process(t.Context(), sess, []tools.ToolCall{{
		ID:       "r",
		Function: tools.FunctionCall{Name: "leaky", Arguments: "{}"},
	}}, []tools.Tool{tool}, em)

	require.Len(t, em.responses, 1)
	assert.Equal(t, original, em.responses[0].Output)
	require.Len(t, em.messages, 1)
	assert.Equal(t, original, em.messages[0].Message.Content)
}

// TestDispatcher_ToolResponseTransformAppliesToErrorResponse covers
// the synthesised-error path: rejection / hook-block / cancellation
// messages also flow through tool_response_transform so a configured
// scrubber sees the same payload the runtime would otherwise emit.
// Without this, a permission_request hook that quoted a secret in its
// rejection reason would leak — errorResponse used to bypass the
// rewrite chain entirely.
func TestDispatcher_ToolResponseTransformAppliesToErrorResponse(t *testing.T) {
	a := newAgent()
	sess := session.New()

	tool := tools.Tool{
		Name:    "shell",
		Handler: func(context.Context, tools.ToolCall) (*tools.ToolCallResult, error) { panic("must not run") },
	}

	rewritten := "rejected with [REDACTED] secret"
	hd := &stubHookDispatcher{
		on: map[hooks.EventType]*hooks.Result{
			hooks.EventToolResponseTransform: {Allowed: true, UpdatedToolResponse: &rewritten},
		},
	}

	d := &toolexec.Dispatcher{
		AgentFor: func(*session.Session) *agent.Agent { return a },
		Hooks:    hd,
		Permissions: func(*session.Session) []toolexec.NamedChecker {
			return []toolexec.NamedChecker{
				{Checker: newDenyChecker("shell"), Source: "test policy"},
			}
		},
	}
	em := &captureEmitter{}

	d.Process(t.Context(), sess, []tools.ToolCall{{
		ID:       "x",
		Function: tools.FunctionCall{Name: "shell", Arguments: "{}"},
	}}, []tools.Tool{tool}, em)

	require.Len(t, em.responses, 1)
	assert.True(t, em.responses[0].IsError)
	assert.Equal(t, rewritten, em.responses[0].Output,
		"synthesised error response must also flow through the transform")
}

// TestDispatcher_ConfirmationCarriesToolMetadata verifies that a
// toolset's static [tools.Tool.Metadata] reaches the tool-call
// confirmation event when the user is prompted.
func TestDispatcher_ConfirmationCarriesToolMetadata(t *testing.T) {
	a := newAgent()
	sess := session.New()

	tool := tools.Tool{
		Name:     "shell",
		Metadata: map[string]string{"danger": "high", "category": "exec"},
		Handler:  func(context.Context, tools.ToolCall) (*tools.ToolCallResult, error) { panic("must not run") },
	}

	resume := make(chan toolexec.ResumeRequest, 1)
	d := &toolexec.Dispatcher{
		AgentFor: func(*session.Session) *agent.Agent { return a },
		Resume:   resume,
	}
	em := &captureEmitter{}

	resume <- toolexec.ResumeRequest{Type: toolexec.ResumeTypeReject}

	d.Process(t.Context(), sess, []tools.ToolCall{{
		ID:       "x",
		Function: tools.FunctionCall{Name: "shell", Arguments: "{}"},
	}}, []tools.Tool{tool}, em)

	require.Len(t, em.confirmations, 1)
	require.Len(t, em.confirmationMeta, 1)
	assert.Equal(t, map[string]string{"danger": "high", "category": "exec"}, em.confirmationMeta[0])
}

// TestDispatcher_ConfirmationMergesHookMetadata verifies that a
// permission_request hook can enrich the confirmation metadata and that
// hook keys win over the toolset's own keys on a clash.
func TestDispatcher_ConfirmationMergesHookMetadata(t *testing.T) {
	a := newAgent()
	sess := session.New()

	tool := tools.Tool{
		Name:     "shell",
		Metadata: map[string]string{"danger": "high", "source": "toolset"},
		Handler:  func(context.Context, tools.ToolCall) (*tools.ToolCallResult, error) { panic("must not run") },
	}

	// Hook allows the prompt to proceed (no short-circuit) but contributes
	// metadata, overriding the toolset's "source" key.
	hd := &stubHookDispatcher{
		on: map[hooks.EventType]*hooks.Result{
			hooks.EventPermissionRequest: {
				Allowed:  true,
				Metadata: map[string]string{"source": "hook", "reason": "policy-x"},
			},
		},
	}

	resume := make(chan toolexec.ResumeRequest, 1)
	d := &toolexec.Dispatcher{
		AgentFor: func(*session.Session) *agent.Agent { return a },
		Hooks:    hd,
		Resume:   resume,
	}
	em := &captureEmitter{}

	resume <- toolexec.ResumeRequest{Type: toolexec.ResumeTypeReject}

	d.Process(t.Context(), sess, []tools.ToolCall{{
		ID:       "x",
		Function: tools.FunctionCall{Name: "shell", Arguments: "{}"},
	}}, []tools.Tool{tool}, em)

	require.Len(t, em.confirmationMeta, 1)
	assert.Equal(t, map[string]string{
		"danger": "high",
		"source": "hook",
		"reason": "policy-x",
	}, em.confirmationMeta[0])
}

// TestDispatcher_ConfirmationMetadataNilWhenNoneSupplied verifies that
// the confirmation event carries nil metadata when neither the toolset
// nor a hook contributed any, keeping the wire payload lean.
func TestDispatcher_ConfirmationMetadataNilWhenNoneSupplied(t *testing.T) {
	a := newAgent()
	sess := session.New()

	tool := tools.Tool{
		Name:    "shell",
		Handler: func(context.Context, tools.ToolCall) (*tools.ToolCallResult, error) { panic("must not run") },
	}

	resume := make(chan toolexec.ResumeRequest, 1)
	d := &toolexec.Dispatcher{
		AgentFor: func(*session.Session) *agent.Agent { return a },
		Resume:   resume,
	}
	em := &captureEmitter{}

	resume <- toolexec.ResumeRequest{Type: toolexec.ResumeTypeReject}

	d.Process(t.Context(), sess, []tools.ToolCall{{
		ID:       "x",
		Function: tools.FunctionCall{Name: "shell", Arguments: "{}"},
	}}, []tools.Tool{tool}, em)

	require.Len(t, em.confirmationMeta, 1)
	assert.Nil(t, em.confirmationMeta[0])
}

// TestDispatcher_SafetyCheckAskPreemptsYolo pins the load-bearing
// property of the safety_check event: an Ask verdict must route to
// user confirmation even when ToolsApproved (--yolo / Allow All)
// would otherwise auto-run the call. Also verifies the hook's
// Metadata reaches the confirmation event.
func TestDispatcher_SafetyCheckAskPreemptsYolo(t *testing.T) {
	a := newAgent()
	sess := session.New()
	sess.ToolsApproved = true

	tool := tools.Tool{
		Name: "shell",
		Handler: func(context.Context, tools.ToolCall) (*tools.ToolCallResult, error) {
			panic("must not run before approval")
		},
	}

	hd := &stubHookDispatcher{
		on: map[hooks.EventType]*hooks.Result{
			hooks.EventSafetyCheck: {
				Allowed:        true,
				Decision:       hooks.DecisionAsk,
				DecisionReason: "rm -rf <path>: irreversible",
				Metadata: map[string]string{
					"blast_radius": "high",
					"category":     "fs-delete",
				},
			},
		},
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	d := &toolexec.Dispatcher{
		AgentFor: func(*session.Session) *agent.Agent { return a },
		Hooks:    hd,
	}
	em := &captureEmitter{confirmed: make(chan struct{})}
	go func() {
		<-em.confirmed
		cancel()
	}()

	d.Process(ctx, sess, []tools.ToolCall{{
		ID:       "danger",
		Function: tools.FunctionCall{Name: "shell", Arguments: `{"cmd":"rm -rf /tmp/x"}`},
	}}, []tools.Tool{tool}, em)

	require.Len(t, em.confirmations, 1, "Ask must route to user confirmation despite --yolo")
	require.Len(t, em.confirmationMeta, 1)
	assert.Equal(t, "high", em.confirmationMeta[0]["blast_radius"],
		"safety_check Metadata must reach the confirmation event")
	assert.Equal(t, "fs-delete", em.confirmationMeta[0]["category"])
	require.Len(t, em.responses, 1)
	assert.True(t, em.responses[0].IsError)
	assert.Contains(t, em.responses[0].Output, "canceled by the user")
}

// TestDispatcher_SafetyCheckDenyShortCircuits pins the Deny path: a
// safety_check Deny verdict goes straight to errorResponse without
// emitting a confirmation event or consulting permission_request.
func TestDispatcher_SafetyCheckDenyShortCircuits(t *testing.T) {
	a := newAgent()
	sess := session.New()
	sess.ToolsApproved = true

	tool := tools.Tool{
		Name: "shell",
		Handler: func(context.Context, tools.ToolCall) (*tools.ToolCallResult, error) {
			panic("must not run when denied")
		},
	}

	hd := &stubHookDispatcher{
		on: map[hooks.EventType]*hooks.Result{
			hooks.EventSafetyCheck: {
				Allowed:        false,
				Decision:       hooks.DecisionDeny,
				DecisionReason: "blocked by policy",
			},
		},
	}

	d := &toolexec.Dispatcher{
		AgentFor: func(*session.Session) *agent.Agent { return a },
		Hooks:    hd,
	}
	em := &captureEmitter{}

	d.Process(t.Context(), sess, []tools.ToolCall{{
		ID:       "x",
		Function: tools.FunctionCall{Name: "shell", Arguments: `{"cmd":"rm -rf /tmp/x"}`},
	}}, []tools.Tool{tool}, em)

	assert.Empty(t, em.confirmations, "Deny must not emit a confirmation event")
	require.Len(t, em.responses, 1)
	assert.True(t, em.responses[0].IsError)
	assert.Contains(t, em.responses[0].Output, "safety_check hook")
	assert.Contains(t, em.responses[0].Output, "blocked by policy")
}

// TestDispatcher_SafetyCheckAllowIsAdvisory pins the Allow path: an
// Allow verdict from safety_check is informational, the pipeline
// still proceeds. With ToolsApproved=true the call must auto-run.
func TestDispatcher_SafetyCheckAllowIsAdvisory(t *testing.T) {
	a := newAgent()
	sess := session.New()
	sess.ToolsApproved = true

	ran := false
	tool := tools.Tool{
		Name: "shell",
		Handler: func(context.Context, tools.ToolCall) (*tools.ToolCallResult, error) {
			ran = true
			return tools.ResultSuccess("ok"), nil
		},
	}

	hd := &stubHookDispatcher{
		on: map[hooks.EventType]*hooks.Result{
			hooks.EventSafetyCheck: {
				Allowed:  true,
				Decision: hooks.DecisionAllow,
			},
		},
	}

	d := &toolexec.Dispatcher{
		AgentFor: func(*session.Session) *agent.Agent { return a },
		Hooks:    hd,
	}
	em := &captureEmitter{}

	d.Process(t.Context(), sess, []tools.ToolCall{{
		ID:       "x",
		Function: tools.FunctionCall{Name: "shell", Arguments: `{"cmd":"ls /tmp"}`},
	}}, []tools.Tool{tool}, em)

	assert.True(t, ran, "Allow from safety_check is advisory; --yolo must still allow the call")
	assert.Empty(t, em.confirmations)
}
