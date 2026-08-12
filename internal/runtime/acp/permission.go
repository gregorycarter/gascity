package acp

import (
	"encoding/json"
	"fmt"
)

// MethodSessionRequestPermission is the ACP method an agent calls on the
// client when a tool call needs authorization.
const MethodSessionRequestPermission = "session/request_permission"

// JSON-RPC 2.0 reserved error codes used when answering agent requests.
const (
	jsonRPCInvalidParams  = -32602
	jsonRPCInternalError  = -32603
	jsonRPCMethodNotFound = -32601
)

// Permission option kinds an agent may offer for a tool call.
const (
	PermissionKindAllowOnce    = "allow_once"
	PermissionKindAllowAlways  = "allow_always"
	PermissionKindRejectOnce   = "reject_once"
	PermissionKindRejectAlways = "reject_always"
)

// PermissionOutcomeSelected identifies an option selected by the client.
const PermissionOutcomeSelected = "selected"

// PermissionOption is one choice the agent offers for a permission request.
type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// RequestPermissionParams is the params for a "session/request_permission"
// request. The tool call itself is deliberately not modeled: the client
// selects from the options the agent supplies and does not interpret the call.
type RequestPermissionParams struct {
	SessionID string             `json:"sessionId"`
	ToolCall  json.RawMessage    `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

// PermissionOutcome reports which option the client selected, if any.
type PermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

// RequestPermissionResult is the result of a "session/request_permission"
// request.
type RequestPermissionResult struct {
	Outcome PermissionOutcome `json:"outcome"`
}

// selectPermissionOption chooses only from options the agent supplied. A
// session must explicitly opt into automatic approval; otherwise the client
// chooses the least-persistent rejection option. Cancellation is not a denial:
// ACP reserves it for a prompt turn that the client actually canceled.
func selectPermissionOption(options []PermissionOption, autoApprove bool) (PermissionOption, bool) {
	kinds := []string{PermissionKindRejectOnce, PermissionKindRejectAlways}
	if autoApprove {
		kinds = []string{
			PermissionKindAllowOnce,
			PermissionKindAllowAlways,
			PermissionKindRejectOnce,
			PermissionKindRejectAlways,
		}
	}
	for _, kind := range kinds {
		for _, opt := range options {
			if opt.Kind == kind && opt.OptionID != "" {
				return opt, true
			}
		}
	}
	return PermissionOption{}, false
}

// errorResponse builds a JSON-RPC error response correlated to id.
func errorResponse(id JSONRPCID, code int, message string) JSONRPCMessage {
	return JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      append(JSONRPCID(nil), id...),
		Error:   &JSONRPCError{Code: code, Message: message},
	}
}

func validatePermissionRequest(params RequestPermissionParams, sessionID string) error {
	if params.SessionID == "" {
		return fmt.Errorf("sessionId is required")
	}
	if sessionID == "" || params.SessionID != sessionID {
		return fmt.Errorf("sessionId %q does not match this session", params.SessionID)
	}
	if len(params.ToolCall) == 0 {
		return fmt.Errorf("toolCall is required")
	}
	var toolCall struct {
		ToolCallID string `json:"toolCallId"`
	}
	if err := json.Unmarshal(params.ToolCall, &toolCall); err != nil || toolCall.ToolCallID == "" {
		return fmt.Errorf("toolCall must contain toolCallId")
	}
	if len(params.Options) == 0 {
		return fmt.Errorf("options must contain at least one option")
	}
	return nil
}

// permissionResponse builds the reply to a "session/request_permission"
// request. Every path returns a message: the agent is blocked on this reply.
func permissionResponse(id JSONRPCID, raw json.RawMessage, sessionID string, autoApprove bool) JSONRPCMessage {
	var params RequestPermissionParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return errorResponse(id, jsonRPCInvalidParams, fmt.Sprintf("session/request_permission params: %v", err))
	}
	if err := validatePermissionRequest(params, sessionID); err != nil {
		return errorResponse(id, jsonRPCInvalidParams, fmt.Sprintf("session/request_permission params: %v", err))
	}

	opt, ok := selectPermissionOption(params.Options, autoApprove)
	if !ok {
		return errorResponse(id, jsonRPCInvalidParams, "session/request_permission params: options contain no selectable result for this approval policy")
	}
	outcome := PermissionOutcome{Outcome: PermissionOutcomeSelected, OptionID: opt.OptionID}

	result, err := json.Marshal(RequestPermissionResult{Outcome: outcome})
	if err != nil {
		return errorResponse(id, jsonRPCInternalError, fmt.Sprintf("marshal permission result: %v", err))
	}
	return JSONRPCMessage{JSONRPC: "2.0", ID: append(JSONRPCID(nil), id...), Result: result}
}

// handleIncomingRequest answers an agent->client request. An agent that issues
// a request blocks its turn until the client replies, so an unhandled method
// must still produce an error response rather than silence.
//
// The reply is written off the read loop: dispatch runs on readLoop, and a
// blocked write to a full stdin pipe would stop stdout from being drained,
// deadlocking agent and client against each other.
func (sc *sessionConn) handleIncomingRequest(msg JSONRPCMessage) {
	var resp JSONRPCMessage
	switch msg.Method {
	case MethodSessionRequestPermission:
		sc.mu.Lock()
		sessionID := sc.sessionID
		autoApprove := sc.autoApprovePermissionRequests
		sc.mu.Unlock()
		resp = permissionResponse(msg.ID, msg.Params, sessionID, autoApprove)
	default:
		resp = errorResponse(msg.ID, jsonRPCMethodNotFound, fmt.Sprintf("method %q is not implemented by this client", msg.Method))
	}

	if !sc.enqueueResponse(resp) {
		select {
		case <-sc.done:
			return
		default:
			sc.failResponseQueue(msg.Method)
		}
	}
}

// sendResponse writes a JSON-RPC response to the agent. Framing is identical
// to a notification — marshal and write one line — and neither registers a
// response waiter, since a response is itself terminal.
func (sc *sessionConn) sendResponse(msg JSONRPCMessage) error {
	return sc.sendNotification(msg)
}
