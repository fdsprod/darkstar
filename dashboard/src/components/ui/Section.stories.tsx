import type { Meta, StoryObj } from "@storybook/react-vite";

import { SectionHeader } from "../InteractionPatterns";
import { DescriptionList, DetailSection, ScopeBadge, VisuallyHidden } from "./Section";

const meta = { title: "UI/DetailSection", component: DetailSection } satisfies Meta<typeof DetailSection>;
export default meta;

export const Default: StoryObj = {
  render: () => <DetailSection>
    <SectionHeader eyebrow="Immutable requested authority" title="Workspace & permissions" meta={<ScopeBadge>Scoped</ScopeBadge>} />
    <DescriptionList entries={[
      { term: "Repository", detail: "darkstar" },
      { term: "Checkout", detail: "main" },
      { term: "Worktree branch", detail: "work/DS-004-toolchain" },
      { term: "Granted tools", detail: "read, write, run" },
    ]} />
  </DetailSection>,
};

export const Badges: StoryObj = {
  render: () => <div style={{ display: "flex", gap: 8 }}>
    <ScopeBadge>Scoped</ScopeBadge>
    <ScopeBadge tone="warning">Elevated</ScopeBadge>
    <ScopeBadge tone="danger">Denied</ScopeBadge>
    <span>Hidden label follows<VisuallyHidden> (announced to screen readers only)</VisuallyHidden></span>
  </div>,
};
