package leantui

import "github.com/docker/docker-agent/pkg/runtime"

// usageSnapshot is the per-session token and cost usage summarized in the
// status footer.
type usageSnapshot struct {
	contextLength int64
	contextLimit  int64
	tokens        int64
	cost          float64
}

// usageTracker aggregates per-session token usage so the footer can show the
// active session's context window alongside the total cost of the whole run
// (the root session plus any nested agent sessions). It keeps a stack of
// in-flight sessions so the "active" session is whichever stream is on top.
type usageTracker struct {
	bySession       map[string]usageSnapshot
	rootSessionID   string
	latestSessionID string
	stack           []string
}

func newUsageTracker() *usageTracker {
	return &usageTracker{bySession: map[string]usageSnapshot{}}
}

func (u *usageTracker) reset() {
	u.bySession = map[string]usageSnapshot{}
	u.rootSessionID = ""
	u.latestSessionID = ""
	u.stack = nil
}

// streamStarted pushes a newly-started session onto the active stack, adopting
// the first one as the root session.
func (u *usageTracker) streamStarted(sessionID string) {
	if sessionID == "" {
		return
	}
	if len(u.stack) == 0 {
		u.rootSessionID = sessionID
	}
	u.stack = append(u.stack, sessionID)
}

// streamStopped pops the most recently-started session off the active stack.
func (u *usageTracker) streamStopped() {
	if n := len(u.stack); n > 0 {
		u.stack = u.stack[:n-1]
	}
}

// record stores usage for a session, adopting the first session seen as the
// root when no stream has started yet.
func (u *usageTracker) record(sessionID string, snapshot usageSnapshot) {
	if u.rootSessionID == "" && len(u.bySession) == 0 {
		u.rootSessionID = sessionID
	}
	u.bySession[sessionID] = snapshot
	u.latestSessionID = sessionID
}

func (u *usageTracker) empty() bool { return len(u.bySession) == 0 }

func (u *usageTracker) totalCost() float64 {
	var total float64
	for _, usage := range u.bySession {
		total += usage.cost
	}
	return total
}

// active returns the usage of the session whose context the footer should show:
// the top of the active stack, else the root, else the most recent, else the
// sole recorded session.
func (u *usageTracker) active() (usageSnapshot, bool) {
	if n := len(u.stack); n > 0 {
		usage, ok := u.bySession[u.stack[n-1]]
		return usage, ok
	}
	if u.rootSessionID != "" {
		usage, ok := u.bySession[u.rootSessionID]
		return usage, ok
	}
	if u.latestSessionID != "" {
		usage, ok := u.bySession[u.latestSessionID]
		return usage, ok
	}
	if len(u.bySession) == 1 {
		for _, usage := range u.bySession {
			return usage, true
		}
	}
	return usageSnapshot{}, false
}

// trackStreamStarted records a newly-started stream and refreshes the footer.
func (m *model) trackStreamStarted(sessionID string) {
	m.usage.streamStarted(sessionID)
	m.applyUsageSnapshot()
}

// trackStreamStopped records a finished stream and refreshes the footer.
func (m *model) trackStreamStopped() {
	m.usage.streamStopped()
	m.applyUsageSnapshot()
}

func (m *model) setTokenUsage(sessionID string, usage *runtime.Usage) {
	if usage == nil {
		return
	}

	snapshot := usageSnapshot{
		contextLength: usage.ContextLength,
		contextLimit:  usage.ContextLimit,
		tokens:        usage.InputTokens + usage.OutputTokens,
		cost:          usage.Cost,
	}
	if sessionID == "" {
		// Once session-scoped usage exists, it is authoritative for the chat
		// footer. Empty-session usage comes from side work such as RAG indexing.
		if m.usage.empty() {
			m.applyStatusUsage(snapshot, usage.Cost, true)
		}
		return
	}
	m.usage.record(sessionID, snapshot)
	m.applyUsageSnapshot()
}

// applyUsageSnapshot pushes the tracker's derived footer usage onto the status
// line: the active session's context window plus the run's total cost.
func (m *model) applyUsageSnapshot() {
	if m.usage.empty() {
		return
	}

	totalCost := m.usage.totalCost()
	if usage, ok := m.usage.active(); ok {
		m.applyStatusUsage(usage, totalCost, true)
		return
	}

	m.status.cost = totalCost
	m.status.costKnown = true
}

func (m *model) applyStatusUsage(usage usageSnapshot, cost float64, costKnown bool) {
	m.status.contextLength = usage.contextLength
	m.status.contextLimit = usage.contextLimit
	m.status.tokens = usage.tokens
	m.status.cost = cost
	m.status.costKnown = costKnown
}
