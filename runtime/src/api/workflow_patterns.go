package api

import (
	"net/http"

	"darkstar/src/core/workflow"
)

func serveAssessmentRouterPattern(response http.ResponseWriter, request *http.Request, requestID string) {
	if request.Method != http.MethodPost {
		writeWorkflowMethod(response, requestID, "POST")
		return
	}
	var input workflow.AssessmentRouterPattern
	if err := decodeWorkflowJSON(request, &input); err != nil {
		writeWorkflowError(response, requestID, err)
		return
	}
	result, err := workflow.BuildAssessmentRouter(input)
	if err != nil {
		writeWorkflowError(response, requestID, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}
