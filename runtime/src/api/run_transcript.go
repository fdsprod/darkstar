package api

import (
	"context"
	"darkstar/src/ports/statestore"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"darkstar/src/core/runexecution"
)

type runHistoryService interface {
	Transcript(context.Context, string, uint64, int) (runexecution.TranscriptPage, error)
	Artifacts(context.Context, string, uint64, int) (runexecution.RunArtifactPage, error)
}

func (s *Server) serveRunGuidance(w http.ResponseWriter, r *http.Request, requestID string, runs RunService, runID string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeAPIError(w, 405, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "Use POST.", RequestID: requestID})
		return
	}
	key, ok := requireIdempotencyKey(w, r, requestID)
	if !ok {
		return
	}
	version, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		writeAPIError(w, 400, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: err.Error(), RequestID: requestID})
		return
	}
	var body struct {
		Message string `json:"message"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 32769))
	decoder.DisallowUnknownFields()
	if !runIDPattern.MatchString(runID) || r.URL.RawQuery != "" || decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF {
		writeAPIError(w, 400, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "Provide one message.", RequestID: requestID})
		return
	}
	guide, ok := runs.(interface {
		Guide(context.Context, runexecution.ControlRequest, string) (runexecution.GuidanceResult, error)
	})
	if !ok {
		writeAPIError(w, 503, apiError{SchemaVersion: 1, Code: "UNAVAILABLE", Message: "Agent messaging is unavailable.", RequestID: requestID})
		return
	}
	result, err := guide.Guide(r.Context(), runexecution.ControlRequest{RunID: runID, ExpectedResourceVersion: version, IdempotencyKey: key, Actor: statestore.Actor{Type: statestore.ActorUser, ID: "local-user"}}, body.Message)
	if err != nil {
		writeAPIError(w, 409, apiError{SchemaVersion: 1, Code: "GUIDANCE_CONFLICT", Message: err.Error(), RequestID: requestID})
		return
	}
	writeJSON(w, 200, result)
}
func (s *Server) serveRunHistory(w http.ResponseWriter, r *http.Request, requestID string, runs RunService, runID, kind string) {
	if !runIDPattern.MatchString(runID) {
		writeAPIError(w, 404, apiError{SchemaVersion: 1, Code: "NOT_FOUND", Message: "Run not found.", RequestID: requestID})
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeAPIError(w, 405, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "Use GET.", RequestID: requestID})
		return
	}
	q := r.URL.Query()
	after := uint64(0)
	limit := 500
	valid := true
	for key, values := range q {
		if len(values) != 1 || (key != "after" && key != "limit") {
			valid = false
		}
	}
	if q.Has("after") {
		var err error
		after, err = strconv.ParseUint(q.Get("after"), 10, 64)
		valid = valid && err == nil
	}
	if q.Has("limit") {
		var err error
		limit, err = strconv.Atoi(q.Get("limit"))
		valid = valid && err == nil
	}
	if !valid || limit < 1 || limit > 1000 {
		writeAPIError(w, 400, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "Use a nonnegative after cursor and a limit from 1 to 1000.", RequestID: requestID})
		return
	}
	reader, ok := runs.(runHistoryService)
	if !ok {
		writeAPIError(w, 503, apiError{SchemaVersion: 1, Code: "UNAVAILABLE", Message: "Run history is unavailable.", RequestID: requestID})
		return
	}
	var value any
	var err error
	if kind == "transcript" {
		value, err = reader.Transcript(r.Context(), runID, after, limit)
	} else {
		value, err = reader.Artifacts(r.Context(), runID, after, limit)
	}
	if err != nil {
		writeRunControlError(w, requestID, err)
		return
	}
	writeJSON(w, 200, value)
}
