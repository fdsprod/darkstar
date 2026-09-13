package githubissues

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"darkstar/src/ports"
)

const maxResponseBytes = 8 << 20

type graphError struct {
	Type       string `json:"type"`
	Extensions struct {
		Code string `json:"code"`
	} `json:"extensions"`
}

func (a *Adapter) graphql(ctx context.Context, query string, variables map[string]any, result any, evidenceRefs ...*string) error {
	// Verify the selected native account in every request, including a token
	// rotated between capability observation and ticket fetch.
	opening := strings.Index(query, "{")
	query = query[:opening+1] + "viewer{id} " + query[opening+1:]
	token, err := a.options.Credentials.Resolve(ctx, a.config.CredentialRef)
	if errors.Is(ctx.Err(), context.Canceled) {
		return fail(ports.FailureCancelled, "GitHub source request was cancelled")
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fail(ports.FailureTimeout, "GitHub source request timed out")
	}
	if err != nil || strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return fail(ports.FailureUnauthenticated, "GitHub protected credentials are unavailable")
	}
	encoded, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return fail(ports.FailureInvalidRequest, "GitHub query parameters are invalid")
	}
	endpoint := "https://api.github.com/graphql"
	if a.config.Host != "github.com" {
		endpoint = "https://" + a.config.Host + "/api/graphql"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return fail(ports.FailureInvalidRequest, "GitHub endpoint is invalid")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := a.options.HTTPClient.Do(request)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return fail(ports.FailureCancelled, "GitHub source request was cancelled")
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fail(ports.FailureTimeout, "GitHub source request timed out")
		}
		return &ports.Failure{Code: ports.FailureUnavailable, Message: "GitHub source could not be reached", Retryable: true}
	}
	defer func() {
		_ = response.Body.Close()
	}()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return fail(ports.FailureUnavailable, "GitHub source response is incomplete or exceeds the size limit")
	}
	if response.StatusCode != http.StatusOK {
		return responseFailure(response.StatusCode, response.Header, body, a.options.Now())
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []graphError    `json:"errors"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return fail(ports.FailureProtocolDrift, "GitHub response was not valid GraphQL JSON")
	}
	// Partial GraphQL data is never projected as a complete successful page.
	if len(envelope.Errors) > 0 {
		for _, problem := range envelope.Errors {
			classification := problem.Type
			if classification == "" {
				classification = problem.Extensions.Code
			}
			switch strings.ToUpper(classification) {
			case "RATE_LIMITED", "RATELIMITED":
				return responseFailure(http.StatusTooManyRequests, response.Header, nil, a.options.Now())
			case "FORBIDDEN", "NOT_FOUND":
				return fail(ports.FailurePermissionDenied, "GitHub resource is missing or inaccessible to this account")
			case "UNAUTHORIZED", "UNAUTHENTICATED":
				return fail(ports.FailureUnauthenticated, "GitHub authentication is no longer valid")
			}
		}
		return fail(ports.FailureProtocolDrift, "GitHub GraphQL response contains errors or incomplete data")
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" || json.Unmarshal(envelope.Data, result) != nil {
		return fail(ports.FailureProtocolDrift, "GitHub GraphQL data is missing or invalid")
	}
	var authority struct {
		Viewer identity `json:"viewer"`
	}
	if json.Unmarshal(envelope.Data, &authority) != nil || authority.Viewer.ID == "" || (a.config.AccountID != "" && authority.Viewer.ID != a.config.AccountID) {
		return fail(ports.FailurePermissionDenied, "GitHub credential authority differs from the configured account")
	}
	evidence, err := a.retain(ctx, "graphql-response", json.RawMessage(body))
	if err != nil {
		return err
	}
	for _, ref := range evidenceRefs {
		*ref = evidence
	}
	return nil
}

func responseFailure(status int, headers http.Header, body []byte, now time.Time) error {
	limited := status == http.StatusTooManyRequests || (status == http.StatusForbidden && (headers.Get("X-RateLimit-Remaining") == "0" || headers.Get("Retry-After") != "" || bytes.Contains(bytes.ToLower(body), []byte("rate limit"))))
	if limited {
		details := map[string]string{}
		var retryAt time.Time
		if seconds, err := strconv.ParseInt(headers.Get("Retry-After"), 10, 64); err == nil && seconds >= 0 {
			details["retry_after_seconds"] = strconv.FormatInt(seconds, 10)
			// Avoid overflowing time.Duration on an invalid provider header.
			if seconds <= int64((365*24*time.Hour)/time.Second) {
				retryAt = now.Add(time.Duration(seconds) * time.Second)
			}
		} else if retry, err := http.ParseTime(headers.Get("Retry-After")); err == nil && retry.After(now) {
			retryAt = retry
		}
		if reset, err := strconv.ParseInt(headers.Get("X-RateLimit-Reset"), 10, 64); err == nil && reset > now.Unix() {
			resetAt := time.Unix(reset, 0)
			if resetAt.After(retryAt) {
				retryAt = resetAt
			}
		}
		if !retryAt.IsZero() {
			details["retry_at_utc"] = retryAt.UTC().Format(time.RFC3339)
			details["retry_after_seconds"] = strconv.FormatInt(int64(retryAt.Sub(now).Seconds()+0.999), 10)
		}
		if len(details) == 0 {
			// GitHub's documented secondary-limit minimum; the daemon owns
			// subsequent exponential backoff and bounded retry scheduling.
			details["retry_after_seconds"] = "60"
		}
		details["backoff"] = "exponential"
		return &ports.Failure{Code: ports.FailureResourceExhausted, Message: "GitHub rate limit reached", Retryable: true, Details: details}
	}
	switch {
	case status == http.StatusUnauthorized:
		return fail(ports.FailureUnauthenticated, "GitHub authentication is no longer valid")
	case status == http.StatusForbidden || status == http.StatusNotFound:
		return fail(ports.FailurePermissionDenied, "GitHub resource is missing or inaccessible to this account")
	case status >= 500:
		return &ports.Failure{Code: ports.FailureUnavailable, Message: "GitHub source is temporarily unavailable", Retryable: true}
	default:
		return fail(ports.FailureProtocolDrift, "GitHub source returned an unsupported response")
	}
}
