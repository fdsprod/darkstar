import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";

import { ContextPanel, ContextTabs, type ContextTab } from "./ContextTabs";
import { Icon, type IconName } from "./Icon";
import { ActionBar, ActionGuidance, DiagnosticsDetails, SectionHeader, StatusBadge } from "./InteractionPatterns";
import { Button } from "./ui/Button";
import { DescriptionList } from "./ui/Section";

const meta = { title: "Patterns/Chrome" } satisfies Meta;
export default meta;

export const Badges: StoryObj = {
  render: () => <div style={{ display: "flex", flexWrap: "wrap", gap: 8 }}>
    {["neutral", "readonly", "success", "warning", "stale", "danger", "error"].map((tone) =>
      <StatusBadge key={tone} tone={tone}>{tone}</StatusBadge>)}
  </div>,
};

export const Section: StoryObj = {
  render: () => <SectionHeader
    eyebrow="Durable decision"
    title="Provider response"
    meta={<StatusBadge tone="success">Approved</StatusBadge>}
    actions={<><Button size="compact">Export</Button><Button size="compact" variant="primary">Re-run</Button></>}
  />,
};

export const Actions: StoryObj = {
  render: () => <ActionBar><Button size="compact">Cancel</Button><Button variant="danger" size="compact">Reject</Button><Button variant="primary">Approve</Button></ActionBar>,
};

export const Guidance: StoryObj = {
  render: () => <ActionGuidance>Publishing a workflow version is human-only and cannot be delegated to an agent.</ActionGuidance>,
};

export const Diagnostics: StoryObj = {
  render: () => <DiagnosticsDetails>
    <DescriptionList entries={[
      { term: "Resource version", detail: "7f3c1e9a" },
      { term: "Global position", detail: "184203" },
      { term: "Route digest", detail: "sha256:2b91…c40e" },
    ]} />
  </DiagnosticsDetails>,
};

// Tabs are controlled: the story owns selection, exactly as a page wrapper does.
export const Tabs: StoryObj = {
  render: function TabsStory() {
    type Tab = "overview" | "evidence" | "activity";
    const [active, setActive] = useState<Tab>("overview");
    const tabs: ContextTab<Tab>[] = [
      { id: "overview", label: "Overview" },
      { id: "evidence", label: "Evidence", count: 4 },
      { id: "activity", label: "Activity", count: 12 },
    ];
    return <>
      <ContextTabs tabs={tabs} active={active} onSelect={(tab) => setActive(tab)} />
      {tabs.map((tab) => <ContextPanel key={tab.id} id={tab.id} active={active === tab.id}><p>The {tab.label} panel.</p></ContextPanel>)}
    </>;
  },
};

const names: IconName[] = ["activity", "agents", "arrow-right", "artifact", "board", "checkpoints", "chevron-down", "create", "menu", "search", "settings", "spark", "workflow", "x"];

export const Icons: StoryObj = {
  render: () => <div style={{ display: "grid", gap: 12, gridTemplateColumns: "repeat(auto-fill, minmax(96px, 1fr))" }}>
    {names.map((name) => <div key={name} style={{ display: "grid", gap: 6, justifyItems: "center", color: "#a1aabc" }}>
      <Icon name={name} /><small>{name}</small>
    </div>)}
  </div>,
};
