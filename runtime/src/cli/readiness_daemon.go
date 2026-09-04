package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"darkstar/src/adapters/statestore/sqlite"
	localapi "darkstar/src/api"
	"darkstar/src/core/readinesscontrol"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/statestore"
)

// configureReadiness publishes only query/decision methods through the
// loopback API. Assessment submission remains on the in-process service.
func configureReadiness(server *localapi.Server, database *sqlite.Database, workflows *workflow.Catalog) error {
	service, err := readinesscontrol.New(database, workflows, daemonReadinessValidation{}, localUserReadinessAuthority{})
	if err != nil {
		return err
	}
	return server.SetReadiness(service)
}

type daemonReadinessValidation struct{}

func (daemonReadinessValidation) ReadinessValidationContext(context.Context, statestore.RunProjection) (workflow.RouteContext, string, error) {
	digest := sha256.Sum256([]byte("darkstar.readiness.route-change.require-approval.v1"))
	return workflow.RouteContext{}, hex.EncodeToString(digest[:]), nil
}

type localUserReadinessAuthority struct{}

func (localUserReadinessAuthority) Actor(context.Context) (statestore.Actor, error) {
	return statestore.Actor{Type: statestore.ActorUser, ID: "local-user"}, nil
}
