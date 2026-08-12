package acp

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/testutil"
)

// captureWriteCloser stands in for the agent's stdin pipe so tests can read
// back whatever the client wrote.
type captureWriteCloser struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	written chan struct{}
}

func (w *captureWriteCloser) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if err == nil {
		select {
		case w.written <- struct{}{}:
		default:
		}
	}
	return n, err
}

func (w *captureWriteCloser) Close() error { return nil }

func (w *captureWriteCloser) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// newTestConn builds a sessionConn wired to a capture buffer.
func newTestConn() (*sessionConn, *captureWriteCloser) {
	w := &captureWriteCloser{written: make(chan struct{}, 1)}
	sc := newSessionConn(nil, w, nil, 100, nil)
	sc.sessionID = "sess-1"
	return sc, w
}

// awaitMessage waits for one asynchronous JSON-RPC write without polling.
func awaitMessage(t *testing.T, w *captureWriteCloser) JSONRPCMessage {
	t.Helper()
	select {
	case <-w.written:
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatal("timed out waiting for a response: the incoming request was never answered")
	}

	line := strings.TrimSpace(w.String())
	var msg JSONRPCMessage
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		t.Fatalf("response is not valid JSON-RPC: %v (raw=%q)", err, line)
	}
	return msg
}

func permissionRequest(t *testing.T, id int64, opts []PermissionOption) JSONRPCMessage {
	t.Helper()
	params, err := json.Marshal(RequestPermissionParams{
		SessionID: "sess-1",
		ToolCall:  json.RawMessage(`{"toolCallId":"call-1","title":"write"}`),
		Options:   opts,
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return JSONRPCMessage{JSONRPC: "2.0", ID: numericJSONRPCID(id), Method: MethodSessionRequestPermission, Params: params}
}

// This is the ga-1w2 regression: kimi awaits conn.request_permission at its
// first tool call. dispatch previously handled only notifications and
// responses, so an incoming REQUEST fell through unanswered and the agent's
// turn hung until the process was torn down.
func TestDispatchAnswersSessionRequestPermission(t *testing.T) {
	sc, w := newTestConn()
	sc.autoApprovePermissionRequests = true

	sc.dispatch(permissionRequest(t, 7, []PermissionOption{
		{OptionID: "reject", Name: "Reject", Kind: PermissionKindRejectOnce},
		{OptionID: "allow", Name: "Allow", Kind: PermissionKindAllowOnce},
	}))

	msg := awaitMessage(t, w)

	if id, ok := msg.ID.int64(); !ok || id != 7 {
		t.Fatalf("response ID = %v, want 7 (must correlate with the request)", msg.ID)
	}
	if msg.Error != nil {
		t.Fatalf("unexpected error response: %+v", msg.Error)
	}

	var res RequestPermissionResult
	if err := json.Unmarshal(msg.Result, &res); err != nil {
		t.Fatalf("unmarshal result: %v (raw=%s)", err, msg.Result)
	}
	if res.Outcome.Outcome != PermissionOutcomeSelected {
		t.Fatalf("outcome = %q, want %q", res.Outcome.Outcome, PermissionOutcomeSelected)
	}
	if res.Outcome.OptionID != "allow" {
		t.Fatalf("optionId = %q, want %q (must select the agent's allow option)", res.Outcome.OptionID, "allow")
	}
}

// A permission request is an agent-to-client request, so it may use a string
// JSON-RPC ID. The response must preserve that exact ID rather than dropping
// the valid frame while decoding it as an integer.
func TestReadLoopAnswersPermissionRequestWithStringID(t *testing.T) {
	sc, w := newTestConn()
	sc.autoApprovePermissionRequests = true

	go sc.readLoop(strings.NewReader(`{"jsonrpc":"2.0","id":"permission-7","method":"session/request_permission","params":{"sessionId":"sess-1","toolCall":{"toolCallId":"call-1","title":"write"},"options":[{"optionId":"allow","name":"Allow","kind":"allow_once"}]}}` + "\n"))

	msg := awaitMessage(t, w)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(w.String()), &raw); err != nil {
		t.Fatalf("unmarshal raw response: %v", err)
	}
	if got := string(raw["id"]); got != `"permission-7"` {
		t.Fatalf("response id = %s, want string request ID %q", got, "permission-7")
	}
	if msg.Error != nil {
		t.Fatalf("unexpected error response: %+v", msg.Error)
	}
}

// ACP also permits an explicit null request ID. It is unusual, but a client
// must preserve it when replying instead of treating the frame as a
// notification and leaving the agent blocked.
func TestReadLoopAnswersPermissionRequestWithNullID(t *testing.T) {
	sc, w := newTestConn()
	sc.autoApprovePermissionRequests = true

	go sc.readLoop(strings.NewReader(`{"jsonrpc":"2.0","id":null,"method":"session/request_permission","params":{"sessionId":"sess-1","toolCall":{"toolCallId":"call-1","title":"write"},"options":[{"optionId":"allow","name":"Allow","kind":"allow_once"}]}}` + "\n"))

	msg := awaitMessage(t, w)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(w.String()), &raw); err != nil {
		t.Fatalf("unmarshal raw response: %v", err)
	}
	if got := string(raw["id"]); got != "null" {
		t.Fatalf("response id = %s, want null request ID", got)
	}
	if msg.Error != nil {
		t.Fatalf("unexpected error response: %+v", msg.Error)
	}
}

// Unless startup configuration explicitly authorizes automatic approval, the
// client must select the agent's rejection option. A pending permission prompt
// is not blanket approval merely because it is running headlessly.
func TestDispatchRejectsPermissionByDefault(t *testing.T) {
	sc, w := newTestConn()

	sc.dispatch(permissionRequest(t, 8, []PermissionOption{
		{OptionID: "allow", Name: "Allow", Kind: PermissionKindAllowOnce},
		{OptionID: "reject", Name: "Reject", Kind: PermissionKindRejectOnce},
	}))

	var res RequestPermissionResult
	if err := json.Unmarshal(awaitMessage(t, w).Result, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if res.Outcome.Outcome != PermissionOutcomeSelected || res.Outcome.OptionID != "reject" {
		t.Fatalf("outcome = %+v, want selected reject option", res.Outcome)
	}
}

// A configured automatic grant remains least-persistent when the agent offers
// both choices. A later prompt can still be rejected by changed configuration.
func TestDispatchPrefersAllowOnceOverAllowAlways(t *testing.T) {
	sc, w := newTestConn()
	sc.autoApprovePermissionRequests = true

	sc.dispatch(permissionRequest(t, 1, []PermissionOption{
		{OptionID: "once", Name: "Allow once", Kind: PermissionKindAllowOnce},
		{OptionID: "always", Name: "Always allow", Kind: PermissionKindAllowAlways},
	}))

	var res RequestPermissionResult
	if err := json.Unmarshal(awaitMessage(t, w).Result, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if res.Outcome.OptionID != "once" {
		t.Fatalf("optionId = %q, want %q", res.Outcome.OptionID, "once")
	}
}

// ACP cancellation is reserved for a canceled prompt turn. When a client
// denies an unattended request, it must select the agent's rejection option.
func TestDispatchSelectsRejectWhenNoAllowOptionOffered(t *testing.T) {
	sc, w := newTestConn()

	sc.dispatch(permissionRequest(t, 2, []PermissionOption{
		{OptionID: "reject", Name: "Reject", Kind: PermissionKindRejectOnce},
	}))

	msg := awaitMessage(t, w)
	if msg.Error != nil {
		t.Fatalf("unexpected error response: %+v", msg.Error)
	}
	var res RequestPermissionResult
	if err := json.Unmarshal(msg.Result, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if res.Outcome.Outcome != PermissionOutcomeSelected || res.Outcome.OptionID != "reject" {
		t.Fatalf("outcome = %+v, want selected reject option", res.Outcome)
	}
}

// An agent that offers only allow options cannot be denied by selecting an
// invented option. Return a correlated protocol error rather than treating
// cancellation as denial or leaving the request unanswered.
func TestDispatchRejectsApprovalOnlyOptionsWithProtocolError(t *testing.T) {
	sc, w := newTestConn()

	sc.dispatch(permissionRequest(t, 12, []PermissionOption{
		{OptionID: "allow", Name: "Allow", Kind: PermissionKindAllowOnce},
	}))

	msg := awaitMessage(t, w)
	if msg.Error == nil {
		t.Fatal("want a JSON-RPC error when no rejection option is available")
	}
	if msg.Error.Code != jsonRPCInvalidParams {
		t.Fatalf("error code = %d, want %d", msg.Error.Code, jsonRPCInvalidParams)
	}
}

// An unknown incoming request must fail fast rather than hang. Silence is the
// one outcome that is always wrong.
func TestDispatchRepliesMethodNotFoundForUnknownRequest(t *testing.T) {
	sc, w := newTestConn()

	id := int64(9)
	sc.dispatch(JSONRPCMessage{JSONRPC: "2.0", ID: numericJSONRPCID(id), Method: "fs/read_text_file"})

	msg := awaitMessage(t, w)
	if responseID, ok := msg.ID.int64(); !ok || responseID != 9 {
		t.Fatalf("response ID = %v, want 9", msg.ID)
	}
	if msg.Error == nil {
		t.Fatal("want a JSON-RPC error response for an unhandled method, got a result")
	}
	if msg.Error.Code != jsonRPCMethodNotFound {
		t.Fatalf("error code = %d, want %d", msg.Error.Code, jsonRPCMethodNotFound)
	}
}

// Malformed params must not strand the agent either — it is still a request
// with an ID, so it still requires a reply.
func TestDispatchAnswersPermissionRequestWithMalformedParams(t *testing.T) {
	sc, w := newTestConn()

	id := int64(3)
	sc.dispatch(JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      numericJSONRPCID(id),
		Method:  MethodSessionRequestPermission,
		Params:  json.RawMessage(`{"options": "not-an-array"}`),
	})

	msg := awaitMessage(t, w)
	if responseID, ok := msg.ID.int64(); !ok || responseID != 3 {
		t.Fatalf("response ID = %v, want 3", msg.ID)
	}
	if msg.Error == nil {
		t.Fatal("want an error response for unparseable params, got a result")
	}
	if msg.Error.Code != jsonRPCInvalidParams {
		t.Fatalf("error code = %d, want %d", msg.Error.Code, jsonRPCInvalidParams)
	}
}

// dispatch runs on the read loop. If it wrote to stdin synchronously, a full
// stdin pipe would stall the loop, stop stdout from being drained, and
// deadlock the agent. Answering must not block the caller.
func TestDispatchDoesNotBlockReadLoopOnSlowStdin(t *testing.T) {
	blocked := make(chan struct{})
	sc := &sessionConn{
		stdin:        blockingWriteCloser{release: blocked},
		outputBufMax: 100,
		pending:      make(map[int64]chan JSONRPCMessage),
		sessionID:    "sess-1",
	}

	returned := make(chan struct{})
	go func() {
		sc.dispatch(permissionRequest(t, 5, []PermissionOption{
			{OptionID: "allow", Name: "Allow", Kind: PermissionKindAllowOnce},
		}))
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatal("dispatch blocked on a stalled stdin write — this deadlocks the read loop")
	}
	close(blocked)
}

// A request flood against a blocked stdin must not leave a permanently busy
// ACP agent behind. The bounded queue rejects the overflow and closes stdin so
// the normal process-exit liveness path can recover the session.
func TestDispatchTerminatesStalledSessionWhenResponseQueueFills(t *testing.T) {
	stdin := newClosingBlockingWriteCloser()
	sc := newSessionConn(nil, stdin, nil, 100, nil)
	sc.sessionID = "sess-1"

	for id := 1; id <= responseQueueSize+2; id++ {
		sc.dispatch(permissionRequest(t, int64(id), []PermissionOption{
			{OptionID: "reject", Name: "Reject", Kind: PermissionKindRejectOnce},
		}))
	}

	select {
	case <-stdin.closed:
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatal("response queue overflow did not close stalled stdin")
	}
}

// blockingWriteCloser blocks in Write until release is closed.
type blockingWriteCloser struct{ release chan struct{} }

func (b blockingWriteCloser) Write(p []byte) (int, error) {
	<-b.release
	return len(p), nil
}

func (b blockingWriteCloser) Close() error { return nil }

type closingBlockingWriteCloser struct {
	release   chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
}

func newClosingBlockingWriteCloser() *closingBlockingWriteCloser {
	return &closingBlockingWriteCloser{
		release: make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

func (b *closingBlockingWriteCloser) Write(_ []byte) (int, error) {
	<-b.release
	return 0, io.ErrClosedPipe
}

func (b *closingBlockingWriteCloser) Close() error {
	b.closeOnce.Do(func() {
		close(b.release)
		close(b.closed)
	})
	return nil
}
