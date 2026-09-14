import { useEffect, useRef, useState } from "react";

import { apiClient, ApiRequestError } from "../api/client";
import { ProjectRepositoriesView, type ProjectRepositories, type ProjectRepository, type ProjectRepositoryLoad, type RepositoryEditorDraft, type RepositoryEditorState } from "../components/ProjectRepositories";
import { repositorySettingsDraft, repositorySettingsFromDraft } from "./projectRepositoriesModel";
import "./projectRepositories.css";

export function ProjectRepositoriesPanel({ selectedProjectId, refreshVersion, onSelect, onRegister, onChanged, onDraftOpen }: { selectedProjectId?: string; refreshVersion: number; onSelect(projectId: string): void; onRegister(): void; onChanged(): void; onDraftOpen(open: boolean): void }) {
  const [resource, setResource] = useState<ProjectRepositoryLoad>({ state: "loading" });
  const [editor, setEditor] = useState<RepositoryEditorState>({ state: "closed" });
  const [notice, setNotice] = useState("");
  const [retry, setRetry] = useState(0);
  const key = useRef(commandKey());
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  useEffect(() => {
    onDraftOpen(editor.state !== "closed");
  }, [editor.state, onDraftOpen]);
  useEffect(() => {
    const abort = new AbortController();
    setResource({ state: "loading" });
    apiClient.listProjectRepositories(abort.signal).then((projects) => {
      if (!abort.signal.aborted) {
        setResource({ state: "ready", projects: projects.slice().sort((left, right) => left.project.name.localeCompare(right.project.name)) });
      }
    }).catch((cause) => {
      if (!abort.signal.aborted) {
        setResource({ state: "unavailable", message: repositoryFailure(cause) });
      }
    });
    return () => {
      abort.abort();
    };
  }, [refreshVersion, retry]);

  function edit(draft: RepositoryEditorDraft) {
    key.current = commandKey();
    setEditor({ state: "editing", draft });
    setNotice("");
  }
  function editMember(project: ProjectRepositories, member: ProjectRepository) {
    edit({ kind: "edit", project, member, label: member.membership.label, role: member.membership.role, settings: repositorySettingsDraft(member.membership.settings) });
  }
  async function submit() {
    if (editor.state === "closed" || editor.state === "saving" || editor.state === "reloading" || editor.state === "stale") {
      return;
    }
    const draft = editor.draft;
    setEditor({ state: "saving", draft });
    try {
      const value = await saveDraft(draft, key.current);
      if (!mounted.current) {
        return;
      }
      setEditor({ state: "closed" });
      setResource((current) => ({ state: "ready", projects: [...(current.state === "ready" ? current.projects.filter((project) => project.project.id !== value.project.id) : []), value] }));
      setNotice(draft.kind === "detach" ? "Repository detached. Files, existing runs, and history are preserved." : draft.kind === "create" ? "Project created. You can start planning now." : "Repository settings saved for future runs.");
      onSelect(value.project.id);
      onChanged();
    } catch (cause) {
      if (!mounted.current) {
        return;
      }
      const stale = cause instanceof ApiRequestError && ["REPOSITORY_REVISION_CONFLICT", "PRECONDITION_FAILED"].includes(cause.code);
      setEditor({ state: stale ? "stale" : "failed", draft, message: stale ? "Your draft is preserved below. Another change was saved first. Reload the latest values before making a new edit." : repositoryFailure(cause) });
    }
  }
  async function reloadDraft() {
    if (editor.state !== "stale" || editor.draft.kind === "create") {
      return;
    }
    const draft = editor.draft;
    setEditor({ state: "reloading", draft });
    try {
      const project = await apiClient.getProjectRepositories(draft.project.project.id);
      if (!mounted.current) {
        return;
      }
      setResource((current) => ({ state: "ready", projects: [...(current.state === "ready" ? current.projects.filter((value) => value.project.id !== project.project.id) : []), project] }));
      if (draft.kind === "defaults") {
        edit({ kind: "defaults", project, settings: repositorySettingsDraft(project.defaults) });
      } else if (draft.kind === "attach") {
        edit({ kind: "attach", project, path: "", label: "", role: "read_only", settings: repositorySettingsDraft() });
      } else {
        const member = project.repositories.find((value) => value.repository.id === draft.member.repository.id);
        if (!member) {
          throw new Error("This membership is no longer available. Cancel the draft and refresh projects.");
        }
        if (draft.kind === "detach" && member.membership.status === "active") {
          edit({ kind: "detach", project, member });
        } else {
          editMember(project, member);
        }
      }
      setNotice("Latest project values loaded. Review them before saving a new change.");
    } catch (cause) {
      if (mounted.current) {
        setEditor({ state: "stale", draft, message: repositoryFailure(cause) });
      }
    }
  }

  return <ProjectRepositoriesView resource={resource} selectedProjectId={selectedProjectId} editor={editor} notice={notice}
    onSelect={onSelect} onCreate={() => edit({ kind: "create", name: "" })} onRegister={onRegister} onRetry={() => setRetry((value) => value + 1)}
    onAttach={(project) => edit({ kind: "attach", project, path: "", label: "", role: "read_only", settings: repositorySettingsDraft() })}
    onEdit={editMember} onDetach={(project, member) => edit({ kind: "detach", project, member })}
    onDefaults={(project) => edit({ kind: "defaults", project, settings: repositorySettingsDraft(project.defaults) })}
    onDraft={edit} onSubmit={() => void submit()} onClose={() => setEditor({ state: "closed" })} onReload={() => void reloadDraft()} />;
}

async function saveDraft(draft: RepositoryEditorDraft, key: string): Promise<ProjectRepositories> {
  if (draft.kind === "create") {
    const name = draft.name.trim();
    if (!name) {
      throw new Error("Enter a project name before creating the project.");
    }
    return apiClient.createProject({ name }, key);
  }
  const projectId = draft.project.project.id;
  const version = draft.project.project.resourceVersion;
  if (draft.kind === "defaults") {
    return apiClient.updateProjectRepositoryDefaults(projectId, version, { defaults: repositorySettingsFromDraft(draft.settings) }, key);
  }
  if (draft.kind === "detach") {
    return apiClient.removeProjectRepository(projectId, draft.member.repository.id, version, { expectedMembershipRevision: draft.member.membership.revision }, key);
  }
  const label = draft.label.trim();
  if (!label) {
    throw new Error("Enter a repository label to identify it within this project.");
  }
  if (draft.project.repositories.some((entry) => entry.membership.status === "active" && entry.membership.label === label && (draft.kind !== "edit" || entry.repository.id !== draft.member.repository.id))) {
    throw new Error("Another repository already uses this label. Choose a unique label in this project.");
  }
  const settings = repositorySettingsFromDraft(draft.settings);
  if (draft.kind === "edit") {
    return apiClient.updateProjectRepository(projectId, draft.member.repository.id, version, { label, role: draft.role, settings, expectedMembershipRevision: draft.member.membership.revision }, key);
  }
  const repositoryPath = draft.path.trim();
  if (!/^(?:[A-Za-z]:[\\/]|\\\\[^\\]+\\|\/)/.test(repositoryPath)) {
    throw new Error("Enter an absolute local repository path, such as C:\\Projects\\my-repository.");
  }
  return apiClient.attachProjectRepository(projectId, version, { repositoryPath, label, role: draft.role, settings, expectedMembershipRevision: 0 }, key);
}

function repositoryFailure(cause: unknown): string {
  if (cause instanceof ApiRequestError) {
    if (cause.status === 404 || cause.status === 503 || cause.code === "WORK_SERVICE_UNAVAILABLE") {
      return "The repository service is unavailable. Check that the daemon is running and up to date, then retry.";
    }
    if (cause.code === "REPOSITORY_SELECTION_REQUIRED") {
      return `${cause.message} Check that the local Git repository still exists and is accessible, then retry.`;
    }
    return cause.message;
  }
  return cause instanceof Error ? cause.message : "The daemon could not save this change. Check the connection and retry.";
}

function commandKey(): string {
  return `project-repositories-${crypto.randomUUID()}`;
}
