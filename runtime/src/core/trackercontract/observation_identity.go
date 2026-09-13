package trackercontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"darkstar/src/ports/tracker"
)

// ObservationIdentity gives immutable observations one canonical identity and
// content fingerprint. Refresh time and evidence locator changes do not invent
// new business revisions; all actual ticket fields remain in the digest.
func ObservationIdentity(ticket tracker.Ticket) (id, key, digest string, err error) {
	if _, err := EncodeTicket(ticket); err != nil {
		return "", "", "", err
	}
	refJSON, err := json.Marshal(ticket.Ref)
	if err != nil {
		return "", "", "", err
	}
	identityJSON, err := json.Marshal(struct {
		Ref      tracker.TicketRef
		Revision string
	}{Ref: ticket.Ref, Revision: ticket.Revision})
	if err != nil {
		return "", "", "", err
	}
	ticket.EvidenceRef = "source-observation"
	ticket.Freshness = tracker.Fresh{ObservedAt: time.Unix(0, 0).UTC(), Revision: ticket.Revision}
	content, err := EncodeTicket(ticket)
	if err != nil {
		return "", "", "", err
	}
	return observationHash(identityJSON), observationHash(refJSON), observationHash(content), nil
}

func observationHash(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
