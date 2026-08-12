package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// defaultOutputBufferLines is the default circular buffer size for Peek output.
const defaultOutputBufferLines = 1000

// responseQueueSize bounds agent-to-client replies waiting for a blocked stdin
// pipe. A single writer preserves response order without letting a burst of
// requests create an unbounded number of blocked goroutines.
const responseQueueSize = 64

// sessionConn tracks a running ACP agent process and its JSON-RPC connection.
type sessionConn struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	done     chan struct{}      // closed when process exits
	readDone chan struct{}      // closed after buffered stdout is dispatched
	cancel   context.CancelFunc // cancels in-progress handshake (sentinel only, set by Start)
	listener net.Listener       // control socket for cross-process ops

	mu             sync.Mutex
	sessionID      string
	activePromptID int64 // non-zero when a prompt response is pending
	outputBuf      []string
	outputBufMax   int
	lastActivity   time.Time

	// activityPublisher moves sidecar I/O off the JSON-RPC read loop. It is
	// installed after the handshake seed is durably committed and detached
	// before session metadata is removed.
	activityPublisher       *activityPublisher
	activityPublisherClosed bool

	// stdinMu serializes writes to the agent's stdin pipe. Separate from
	// mu so that a slow/blocked stdin write cannot prevent dispatch (which
	// needs mu) from routing responses, avoiding a circular pipe deadlock.
	stdinMu sync.Mutex

	// nudgeMu serializes Nudge calls so that waitIdle → setActivePrompt →
	// sendRequest is atomic with respect to other Nudge calls.
	nudgeMu sync.Mutex

	// pending tracks response waiters by request ID.
	pending map[int64]chan JSONRPCMessage
	idleCh  chan struct{}

	// autoApprovePermissionRequests is copied from the per-session startup
	// config. It is false unless configuration explicitly selected a
	// non-interactive approval policy.
	autoApprovePermissionRequests bool

	responseWriterOnce  sync.Once
	responseFailureOnce sync.Once
	responseCh          chan JSONRPCMessage
}

// newSessionConn creates a sessionConn with the given buffer size.
func newSessionConn(cmd *exec.Cmd, stdin io.WriteCloser, lis net.Listener, bufSize int, done chan struct{}) *sessionConn {
	if bufSize <= 0 {
		bufSize = defaultOutputBufferLines
	}
	if done == nil {
		done = make(chan struct{})
	}
	sc := &sessionConn{
		cmd:          cmd,
		stdin:        stdin,
		done:         done,
		readDone:     make(chan struct{}),
		listener:     lis,
		outputBufMax: bufSize,
		pending:      make(map[int64]chan JSONRPCMessage),
		idleCh:       make(chan struct{}),
	}
	close(sc.idleCh)
	return sc
}

// readLoop reads JSON-RPC messages from the agent's stdout and dispatches them.
// It runs until the reader returns EOF or an error.
func (sc *sessionConn) readLoop(r io.Reader) {
	defer close(sc.readDone)

	scanner := bufio.NewScanner(r)
	// ACP messages can be large (e.g., file contents in updates).
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var msg JSONRPCMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue // skip non-JSON lines (e.g., startup banners)
		}

		sc.dispatch(msg)
	}

	// readLoop exited (EOF, scanner error, or oversized frame). Log the
	// scanner error if present, then clear busy state and drain pending
	// channels so callers don't hang.
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "acp: readLoop exit: %v\n", err)
	}
	sc.drainPending()
}

// dispatch routes a decoded JSON-RPC message.
func (sc *sessionConn) dispatch(msg JSONRPCMessage) {
	// Notification (no ID): handle session/update.
	if !msg.ID.present() && msg.Method == "session/update" {
		sc.handleUpdate(msg)
		return
	}

	// Request (has ID and method): the agent is asking the client something
	// and blocks its turn until answered.
	if msg.ID.present() && msg.Method != "" {
		sc.handleIncomingRequest(msg)
		return
	}

	// Response (has ID, no method): route to waiter.
	if msg.ID.present() && msg.Method == "" {
		id, ok := msg.ID.int64()
		if !ok {
			// All client-originated requests use numeric IDs. A response with a
			// string or null ID cannot correlate with one of our waiters.
			return
		}
		sc.mu.Lock()
		ch, pending := sc.pending[id]
		if pending {
			delete(sc.pending, id)
		}
		// Clear busy state if this is the active prompt response.
		if sc.activePromptID != 0 && id == sc.activePromptID {
			sc.markIdleLocked()
		}
		sc.mu.Unlock()
		if pending {
			ch <- msg
		}
		return
	}
}

// handleUpdate processes a session/update notification.
func (sc *sessionConn) handleUpdate(msg JSONRPCMessage) {
	var params SessionUpdateParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		fmt.Fprintf(os.Stderr, "acp: session/update unmarshal: %v\n", err)
		return
	}

	sc.markActivity(time.Now())

	sc.mu.Lock()
	defer sc.mu.Unlock()

	switch params.Update.Type {
	case "agent_message_chunk", "user_message_chunk", "agent_thought_chunk":
		var block ContentBlock
		if err := json.Unmarshal(params.Update.Content, &block); err != nil {
			fmt.Fprintf(os.Stderr, "acp: session/update content unmarshal (variant=%s): %v\n", params.Update.Type, err)
			return
		}
		sc.appendContentBlock(block)
	case "tool_call", "tool_call_update":
		if params.Update.Title != "" {
			sc.appendLine("[tool: " + params.Update.Title + "]")
		}
		if len(params.Update.Content) > 0 {
			sc.appendToolCallContent(params.Update.Type, params.Update.Content)
		}
	default:
		if params.Update.Type == "" {
			if len(params.Content) > 0 {
				sc.appendContentBlocks(params.Content)
				return
			}
			fmt.Fprintln(os.Stderr, "acp: session/update missing update discriminator")
		}
	}
}

func (sc *sessionConn) appendToolCallContent(variant string, raw json.RawMessage) {
	var parts []toolCallContent
	if err := json.Unmarshal(raw, &parts); err != nil {
		fmt.Fprintf(os.Stderr, "acp: session/update content unmarshal (variant=%s): %v\n", variant, err)
		return
	}
	for _, part := range parts {
		if part.Type != "content" {
			continue
		}
		var block ContentBlock
		if err := json.Unmarshal(part.Content, &block); err != nil {
			fmt.Fprintf(os.Stderr, "acp: session/update tool content unmarshal (variant=%s): %v\n", variant, err)
			continue
		}
		sc.appendContentBlock(block)
	}
}

func (sc *sessionConn) appendContentBlocks(blocks []ContentBlock) {
	for _, block := range blocks {
		sc.appendContentBlock(block)
	}
}

func (sc *sessionConn) appendContentBlock(block ContentBlock) {
	if block.Type != "text" || block.Text == "" {
		return
	}
	for _, line := range strings.Split(block.Text, "\n") {
		sc.appendLine(line)
	}
}

// appendLine adds a line to the circular output buffer. Caller must hold mu.
func (sc *sessionConn) appendLine(line string) {
	if len(sc.outputBuf) >= sc.outputBufMax {
		// Shift buffer: drop oldest line.
		copy(sc.outputBuf, sc.outputBuf[1:])
		sc.outputBuf[len(sc.outputBuf)-1] = line
	} else {
		sc.outputBuf = append(sc.outputBuf, line)
	}
}

// sendRequest encodes a JSON-RPC message to the agent's stdin and registers
// a response waiter. Returns the response channel.
func (sc *sessionConn) sendRequest(msg JSONRPCMessage) (chan JSONRPCMessage, error) {
	if !msg.ID.present() {
		return nil, sc.sendNotification(msg)
	}
	id, ok := msg.ID.int64()
	if !ok {
		return nil, fmt.Errorf("outgoing ACP request has non-numeric JSON-RPC ID")
	}

	ch := make(chan JSONRPCMessage, 1)
	sc.mu.Lock()
	sc.pending[id] = ch
	sc.mu.Unlock()

	data, err := json.Marshal(msg)
	if err != nil {
		sc.mu.Lock()
		delete(sc.pending, id)
		sc.mu.Unlock()
		return nil, fmt.Errorf("marshal: %w", err)
	}

	sc.stdinMu.Lock()
	_, err = fmt.Fprintf(sc.stdin, "%s\n", data)
	sc.stdinMu.Unlock()
	if err != nil {
		sc.mu.Lock()
		delete(sc.pending, id)
		sc.mu.Unlock()
		return nil, fmt.Errorf("write: %w", err)
	}

	return ch, nil
}

// enqueueResponse queues one agent-to-client response without blocking the
// read loop on a full stdin pipe. The queue is deliberately bounded: an agent
// that floods requests while refusing to read responses cannot make gc retain
// an unbounded goroutine per request.
func (sc *sessionConn) enqueueResponse(msg JSONRPCMessage) bool {
	sc.responseWriterOnce.Do(func() {
		sc.responseCh = make(chan JSONRPCMessage, responseQueueSize)
		go sc.responseLoop()
	})

	select {
	case <-sc.done:
		return false
	case sc.responseCh <- msg:
		return true
	default:
		return false
	}
}

// responseLoop serializes writes from the bounded agent-request response
// queue. Process exit closes the stdin pipe, causing an in-flight write to
// return; done then prevents any further queued response work.
func (sc *sessionConn) responseLoop() {
	for {
		select {
		case <-sc.done:
			return
		case msg := <-sc.responseCh:
			if err := sc.sendResponse(msg); err != nil {
				fmt.Fprintf(os.Stderr, "acp: responding to agent request: %v\n", err)
				return
			}
		}
	}
}

// failResponseQueue tears down a session whose agent has filled the bounded
// response queue while stdin is blocked. Leaving that agent alive would strand
// its pending RPC forever; terminating it lets the normal liveness path heal
// the session instead.
func (sc *sessionConn) failResponseQueue(method string) {
	sc.responseFailureOnce.Do(func() {
		fmt.Fprintf(os.Stderr, "acp: response queue full while answering %s; terminating stalled session\n", method)
		go func() {
			if sc.stdin != nil {
				_ = sc.stdin.Close()
			}
			if sc.cmd == nil {
				return
			}
			if err := terminateProcess(sc); err != nil {
				fmt.Fprintf(os.Stderr, "acp: terminating stalled response queue: %v\n", err)
			}
		}()
	})
}

// sendNotification encodes a JSON-RPC notification (no response expected).
func (sc *sessionConn) sendNotification(msg JSONRPCMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	sc.stdinMu.Lock()
	_, err = fmt.Fprintf(sc.stdin, "%s\n", data)
	sc.stdinMu.Unlock()
	return err
}

// setActivePrompt marks the given request ID as the active prompt.
func (sc *sessionConn) setActivePrompt(id int64) {
	sc.mu.Lock()
	sc.markBusyLocked(id)
	sc.mu.Unlock()
}

// drainPending clears busy state and closes all pending response channels.
// Safe to call multiple times — closed channels are deleted from the map.
func (sc *sessionConn) drainPending() {
	sc.mu.Lock()
	sc.markIdleLocked()
	for id, ch := range sc.pending {
		close(ch)
		delete(sc.pending, id)
	}
	sc.mu.Unlock()
}

func (sc *sessionConn) clearActivePrompt(id int64) {
	sc.mu.Lock()
	if id == 0 || sc.activePromptID == id {
		sc.markIdleLocked()
	}
	sc.mu.Unlock()
}

// isBusy reports whether a prompt response is pending.
func (sc *sessionConn) isBusy() bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.activePromptID != 0
}

func (sc *sessionConn) ensureIdleChannelLocked() {
	if sc.idleCh == nil {
		sc.idleCh = make(chan struct{})
		if sc.activePromptID == 0 {
			close(sc.idleCh)
		}
	}
}

func (sc *sessionConn) markBusyLocked(id int64) {
	sc.ensureIdleChannelLocked()
	if sc.activePromptID == 0 {
		sc.idleCh = make(chan struct{})
	}
	sc.activePromptID = id
}

func (sc *sessionConn) markIdleLocked() {
	sc.ensureIdleChannelLocked()
	sc.activePromptID = 0
	select {
	case <-sc.idleCh:
	default:
		close(sc.idleCh)
	}
}

// waitIdle blocks until the agent is not busy or the timeout expires.
// Returns true if the agent became idle, false on timeout.
func (sc *sessionConn) waitIdle(timeout time.Duration) bool {
	sc.mu.Lock()
	sc.ensureIdleChannelLocked()
	if sc.activePromptID == 0 {
		sc.mu.Unlock()
		return true
	}
	idleCh := sc.idleCh
	sc.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-idleCh:
		return true
	case <-timer.C:
		return false
	}
}

// peekLines returns the last n lines from the output buffer.
// If n <= 0, returns all lines.
func (sc *sessionConn) peekLines(n int) string {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	lines := sc.outputBuf
	if n > 0 && n < len(lines) {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// clearOutput resets the output buffer.
func (sc *sessionConn) clearOutput() {
	sc.mu.Lock()
	sc.outputBuf = sc.outputBuf[:0]
	sc.mu.Unlock()
}

// getLastActivity returns the time of the last session/update notification.
func (sc *sessionConn) getLastActivity() time.Time {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.lastActivity
}

// markActivity records that the agent produced output at t and offers the
// newest stamp to the asynchronous publisher. It performs no filesystem I/O.
func (sc *sessionConn) markActivity(t time.Time) {
	sc.mu.Lock()
	if t.After(sc.lastActivity) {
		sc.lastActivity = t
	}
	stamp := sc.lastActivity
	publisher := sc.activityPublisher
	sc.mu.Unlock()

	if publisher != nil {
		publisher.offer(stamp)
	}
}

// installActivityPublisher attaches a worker after seed has been written.
// Updates observed during the handshake are coalesced behind the seed.
func (sc *sessionConn) installActivityPublisher(publisher *activityPublisher, seed time.Time) error {
	sc.mu.Lock()
	if sc.activityPublisherClosed {
		sc.mu.Unlock()
		publisher.close()
		return fmt.Errorf("ACP connection closed before activity publication started")
	}
	if seed.After(sc.lastActivity) {
		sc.lastActivity = seed
	}
	latest := sc.lastActivity
	sc.activityPublisher = publisher
	sc.mu.Unlock()

	if latest.After(seed) {
		publisher.offer(latest)
	}
	return nil
}

// closeActivityPublisher waits for any in-flight atomic write to finish.
func (sc *sessionConn) closeActivityPublisher() {
	sc.mu.Lock()
	sc.activityPublisherClosed = true
	publisher := sc.activityPublisher
	sc.activityPublisher = nil
	sc.mu.Unlock()
	if publisher != nil {
		publisher.close()
	}
}

// alive reports whether the process is still running.
func (sc *sessionConn) alive() bool {
	select {
	case <-sc.done:
		return false
	default:
		return true
	}
}

// limitedWriter is a thread-safe io.Writer that keeps only the last max bytes.
type limitedWriter struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.max {
		w.buf = w.buf[len(w.buf)-w.max:]
	}
	w.mu.Unlock()
	return len(p), nil
}

// String returns the captured bytes as a string.
func (w *limitedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}
