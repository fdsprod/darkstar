package delivery_test

import (
	"reflect"
	"testing"

	"darkstar/src/ports/delivery"
)

func TestContractChoicesAreClosedInterfaces(t *testing.T) {
	t.Parallel()
	choices := []any{
		delivery.HealthReady{},
		delivery.RemoteBranchMissing{},
		delivery.OwnedRemoteBranchAt{},
		delivery.PublishAfterFinalValidation{},
		delivery.PublishAfterPointAcceptance{},
		delivery.BranchAlreadyPublished{},
		delivery.DraftState{},
		delivery.OwnedChangeRequest{},
		delivery.KeepTitle{},
		delivery.MarkReady{},
		delivery.ChangeRequestReconciled{},
		delivery.ChangeRequestUnchanged{},
		delivery.ChangeRequestMissing{},
	}
	for _, choice := range choices {
		kind := reflect.TypeOf(choice).Kind()
		if kind != reflect.Struct {
			t.Fatalf("choice %T has kind %s, want struct variant", choice, kind)
		}
	}
}

func TestMutationRequestsHaveOneOperationIdentity(t *testing.T) {
	t.Parallel()
	for _, request := range []any{
		delivery.PublishBranchRequest{},
		delivery.CreateChangeRequestRequest{},
		delivery.UpdateChangeRequestRequest{},
	} {
		typeOf := reflect.TypeOf(request)
		if _, found := typeOf.FieldByName("OperationID"); !found {
			t.Fatalf("%s has no OperationID", typeOf)
		}
		if _, found := typeOf.FieldByName("IdempotencyKey"); found {
			t.Fatalf("%s duplicates operation identity with IdempotencyKey", typeOf)
		}
	}
}

func TestOperationContractsDoNotUseBooleanOrPointerState(t *testing.T) {
	t.Parallel()
	contracts := []any{
		delivery.HealthObservation{},
		delivery.BranchObservation{},
		delivery.PublishBranchRequest{},
		delivery.BranchPublication{},
		delivery.ChangeRequest{},
		delivery.FinalValidationAuthorization{},
		delivery.FinalChangeRequestContent{},
		delivery.CreateChangeRequestRequest{},
		delivery.ChangeRequestCreation{},
		delivery.UpdateChangeRequestRequest{},
		delivery.ChangeRequestUpdate{},
		delivery.ChangeRequestObservation{},
	}
	for _, contract := range contracts {
		typeOf := reflect.TypeOf(contract)
		for index := 0; index < typeOf.NumField(); index++ {
			field := typeOf.Field(index)
			if field.Type.Kind() == reflect.Bool || field.Type.Kind() == reflect.Pointer {
				t.Fatalf("%s.%s uses %s state", typeOf, field.Name, field.Type.Kind())
			}
		}
	}
}

func TestFinalCreationAuthorizationCannotRepresentDraftMode(t *testing.T) {
	t.Parallel()
	request := reflect.TypeOf(delivery.CreateChangeRequestRequest{})
	if _, found := request.FieldByName("Mode"); found {
		t.Fatal("final creation request exposes an independent mode")
	}
	if _, found := request.FieldByName("ExpectedHeadSHA"); found {
		t.Fatal("final creation request duplicates its authorized head SHA")
	}
	authorization, found := request.FieldByName("Authorization")
	if !found || authorization.Type != reflect.TypeOf(delivery.FinalValidationAuthorization{}) {
		t.Fatalf("authorization = %v, want concrete FinalValidationAuthorization", authorization.Type)
	}
}
