package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"darkstar/src/core/workflowchat"
)

func (s *Server) SetWorkflowChat(runner workflowchat.Runner) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != serverNew || runner == nil {
		return errors.New("workflow chat runner must be configured before start")
	}
	s.workflowChat = runner
	return nil
}

func (s *Server) serveWorkflowChat(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodPost {
		writeWorkflowMethod(w, requestID, "POST")
		return
	}
	s.mu.RLock()
	runner, store := s.workflowChat, s.workflows
	s.mu.RUnlock()
	if runner == nil || store == nil {
		writeAPIError(w, http.StatusServiceUnavailable, apiError{SchemaVersion: 1, Code: "WORKFLOW_CHAT_UNAVAILABLE", Message: "Workflow chat requires a configured Codex provider.", RequestID: requestID})
		return
	}
	var input workflowchat.Request
	if err := decodeWorkflowJSON(r, &input); err != nil {
		writeWorkflowError(w, requestID, err)
		return
	}
	if err := input.Validate(); err != nil {
		writeWorkflowError(w, requestID, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Accel-Buffering", "no")
	emit := func(kind string, payload any) error {
		if err := json.NewEncoder(w).Encode(map[string]any{"kind": kind, "payload": payload}); err != nil {
			cancel()
			return err
		}
		return http.NewResponseController(w).Flush()
	}
	if err := emit("status", map[string]string{"message": "Inspecting workflow…"}); err != nil {
		return
	}
	session := &workflowchat.Session{Store: store, Target: input.Target, Key: "workflow-chat-" + requestID, Emit: emit, Generation: input.Generation}
	if err := runner.Run(ctx, session, input.Messages, emit); err != nil {
		_ = emit("error", map[string]string{"message": err.Error()})
		return
	}
	_ = emit("done", map[string]string{"message": "Turn complete"})
}

func (s *Server) serveWorkflowChatModels(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodGet {
		writeWorkflowMethod(w, requestID, "GET")
		return
	}
	s.mu.RLock()
	runner := s.workflowChat
	s.mu.RUnlock()
	lister, ok := runner.(workflowchat.ModelLister)
	if !ok {
		writeAPIError(w, http.StatusServiceUnavailable, apiError{SchemaVersion: 1, Code: "WORKFLOW_CHAT_UNAVAILABLE", Message: "Workflow chat model selection is unavailable.", RequestID: requestID})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	models, err := lister.Models(ctx)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, apiError{SchemaVersion: 1, Code: "WORKFLOW_CHAT_MODELS_UNAVAILABLE", Message: err.Error(), RequestID: requestID})
		return
	}
	writeJSON(w, http.StatusOK, models)
}
