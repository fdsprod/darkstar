import { apiClient } from "../api/client";
import type { components } from "../api/schema.generated";

type Schemas = components["schemas"];
const key = (operation: string) => `dashboard-workflow-${operation}-${crypto.randomUUID()}`;

export const workflowEditorApi = {
  library: (signal?: AbortSignal) => apiClient.getWorkflowLibrary(signal),
  catalog: (signal?: AbortSignal) => apiClient.getWorkflowAuthoringCatalog(signal),
  draft: (id: string, signal?: AbortSignal) => apiClient.getWorkflowDraft(id, signal),
  create: (body: Schemas["WorkflowDraftCreateRequest"], signal?: AbortSignal) => apiClient.createWorkflowDraft(body, key("create"), signal),
  duplicate: (body: Schemas["WorkflowDraftDuplicateRequest"], signal?: AbortSignal) => apiClient.duplicateWorkflowDraft(body, key("duplicate"), signal),
  save: (body: Schemas["WorkflowDraftUpdateRequest"], signal?: AbortSignal) => apiClient.updateWorkflowDraft(body, signal),
  rename: (body: Schemas["WorkflowDraftRenameRequest"], signal?: AbortSignal) => apiClient.renameWorkflowDraft(body, signal),
  validate: (body: Schemas["WorkflowDraftRevisionRequest"], signal?: AbortSignal) => apiClient.validateWorkflowDraft(body, signal),
  preview: (body: Schemas["WorkflowDraftPreviewRequest"], signal?: AbortSignal) => apiClient.previewWorkflowDraft(body, signal),
  publish: (body: Schemas["WorkflowDraftPublishRequest"], signal?: AbortSignal) => apiClient.publishWorkflowDraft(body, key("publish"), signal),
  show: (name: string, version: string, signal?: AbortSignal) => apiClient.showWorkflow(name, version, signal),
  archive: (name: string, version: string, signal?: AbortSignal) => apiClient.archiveWorkflowVersion(name, version, key("archive"), signal),
};
