import { apiClient } from "../api/client";
import type { ContentDocument, ContentItem } from "./contentLibraryModel";

export const contentLibraryApi = {
  list(signal?: AbortSignal) {
    return apiClient.operation("listContentLibrary", { signal });
  },
  get(id: string, signal?: AbortSignal) {
    return apiClient.operation("getContentItem", { path: { id }, signal });
  },
  create(name: string, description: string, document: ContentDocument) {
    return apiClient.operation("createContentItem", { body: { name, description, document } });
  },
  save(item: ContentItem, name: string, description: string, document: ContentDocument) {
    return apiClient.operation("updateContentDraft", { path: { id: item.id }, body: { expectedRevision: item.draft.revision, name, description, document } });
  },
  publish(item: ContentItem, version: string) {
    return apiClient.operation("publishContentVersion", { path: { id: item.id }, body: { expectedRevision: item.draft.revision, version } });
  },
  duplicate(item: ContentItem, name: string) {
    return apiClient.operation("duplicateContentItem", { path: { id: item.id }, body: { name } });
  },
  archive(item: ContentItem) {
    return apiClient.operation("archiveContentItem", { path: { id: item.id }, body: {} });
  },
  restore(item: ContentItem) {
    return apiClient.operation("restoreContentItem", { path: { id: item.id }, body: {} });
  },
  preview(document: ContentDocument, linkedInputs: string[], revision: boolean, signal?: AbortSignal) {
    return apiClient.operation("previewContentPrompt", { body: { document, linkedInputs, revision }, signal });
  },
  usages(contentId: string, signal?: AbortSignal) {
    return apiClient.operation("getContentUsages", { query: { contentId }, signal });
  },
};
