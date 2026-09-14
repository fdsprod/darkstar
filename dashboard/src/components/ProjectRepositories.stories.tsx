import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";

import { ProjectRepositoriesView, type ProjectRepositories, type ProjectRepositoriesViewProps, type RepositoryEditorState } from "./ProjectRepositories";
import { repositorySettingsDraft } from "../pages/projectRepositoriesModel";
import "../pages/projectRepositories.css";

function projectFixture(count: number): ProjectRepositories {
  const createdAt = "2026-09-14T08:00:00Z";
  const projectId = "project_00000000000000000000000001";
  return {
    schemaVersion: 2,
    project: { id: projectId, name: "Atlas", sourceHash: "a".repeat(64), status: "active", resourceVersion: count + 1, lastGlobalPosition: count + 1, createdAt, updatedAt: createdAt },
    defaults: { baseRef: "main", pathScope: null, validationProfiles: null }, migration: { state: "ready" },
    repositories: Array.from({ length: count }, (_, index) => ({
      repository: { id: `repository_${String(index + 1).padStart(26, "0")}`, root: `C:\\Projects\\atlas-${["web", "api", "docs"][index]}`, commonGitDir: `C:\\Projects\\atlas-${index}\\.git`, identityKey: `git-${index}`, createdAt },
      membership: { projectId, repositoryId: `repository_${String(index + 1).padStart(26, "0")}`, revision: 1, label: ["Web application", "API service", "Documentation"][index], role: index === 0 ? "implementation" : "read_only", status: "active", settings: { pathScope: null, validationProfiles: null }, updatedAt: createdAt },
    })),
  };
}

const empty = projectFixture(0);
const single = projectFixture(1);
const multiple = projectFixture(3);
const noop = () => {};
const callbacks = { onSelect: noop, onCreate: noop, onRegister: noop, onRetry: noop, onAttach: noop, onEdit: noop, onDetach: noop, onDefaults: noop, onDraft: noop, onSubmit: noop, onClose: noop, onReload: noop };

const meta = {
  title: "Projects/Repositories",
  component: ProjectRepositoriesView,
  parameters: { layout: "padded" },
  args: { ...callbacks, resource: { state: "ready", projects: [empty] }, selectedProjectId: empty.project.id, editor: { state: "closed" } },
} satisfies Meta<typeof ProjectRepositoriesView>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Empty: Story = { args: { resource: { state: "ready", projects: [] }, selectedProjectId: undefined } };
export const WithoutCode: Story = {};
export const OneRepository: Story = { args: { resource: { state: "ready", projects: [single] } } };
export const MultipleRepositories: Story = { args: { resource: { state: "ready", projects: [multiple] } } };
export const Loading: Story = { args: { resource: { state: "loading" } } };
export const Unavailable: Story = { args: { resource: { state: "unavailable", message: "Check that the daemon is running and up to date, then retry." } } };
export const ValidationError: Story = { args: { editor: { state: "failed", draft: { kind: "attach", project: empty, path: "relative/path", label: "Website", role: "read_only", settings: repositorySettingsDraft() }, message: "Enter an absolute local Git repository path, then retry." } } };
export const StaleUpdate: Story = { args: { resource: { state: "ready", projects: [single] }, editor: { state: "stale", draft: { kind: "edit", project: single, member: single.repositories[0], label: "My unsaved name", role: "implementation", settings: repositorySettingsDraft({ baseRef: "next" }) }, message: "Another edit was saved first. Your draft is preserved until you reload the latest values." } } };
export const DetachConfirmation: Story = { args: { resource: { state: "ready", projects: [single] }, editor: { state: "editing", draft: { kind: "detach", project: single, member: single.repositories[0] } } } };

function InteractiveEditor(args: ProjectRepositoriesViewProps) {
  const [editor, setEditor] = useState<RepositoryEditorState>({ state: "closed" });
  const [notice, setNotice] = useState("");
  return <ProjectRepositoriesView {...args} editor={editor} notice={notice}
    onCreate={() => setEditor({ state: "editing", draft: { kind: "create", name: "" } })}
    onAttach={(project) => setEditor({ state: "editing", draft: { kind: "attach", project, path: "", label: "", role: "read_only", settings: repositorySettingsDraft() } })}
    onEdit={(project, member) => setEditor({ state: "editing", draft: { kind: "edit", project, member, label: member.membership.label, role: member.membership.role, settings: repositorySettingsDraft(member.membership.settings) } })}
    onDefaults={(project) => setEditor({ state: "editing", draft: { kind: "defaults", project, settings: repositorySettingsDraft(project.defaults) } })}
    onDetach={(project, member) => setEditor({ state: "editing", draft: { kind: "detach", project, member } })}
    onDraft={(draft) => setEditor({ state: "editing", draft })}
    onClose={() => setEditor({ state: "closed" })}
    onSubmit={() => {
      setEditor({ state: "closed" });
      setNotice("Preview completed. This isolated story does not change a repository.");
    }} />;
}
export const KeyboardForms: Story = { args: { resource: { state: "ready", projects: [multiple] } }, render: (args) => <InteractiveEditor {...args} /> };
