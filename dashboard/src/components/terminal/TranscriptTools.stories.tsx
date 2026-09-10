import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";

import { usage } from "./stories.fixtures";
import { RunViewTabs, TranscriptTools, type RunViewMode } from "./TranscriptTools";
import type { EventKind } from "./transcriptModel";

const meta = {
  title: "Terminal/TranscriptTools",
  component: TranscriptTools,
  parameters: { layout: "fullscreen" },
  decorators: [(Story) => <div className="run-terminal"><Story /></div>],
  args: {
    filter: "", kind: "", following: true, raw: false, exportDisabled: false,
    onFilter: () => undefined, onKind: () => undefined, onFollowLive: () => undefined,
    onRaw: () => undefined, onExport: () => undefined,
  },
} satisfies Meta<typeof TranscriptTools>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Following: Story = {};
export const Paused: Story = { args: { following: false } };
export const Filtered: Story = { args: { filter: "Assert-Toolchain", kind: "tool" as EventKind, following: false } };
export const RawMessages: Story = { args: { raw: true } };
export const LoadingHistory: Story = { args: { exportDisabled: true } };

export const Interactive: Story = {
  render: function InteractiveStory(args) {
    const [filter, setFilter] = useState("");
    const [kind, setKind] = useState<EventKind | "">("");
    const [raw, setRaw] = useState(false);
    const [following, setFollowing] = useState(true);
    return <TranscriptTools {...args} filter={filter} onFilter={setFilter} kind={kind} onKind={setKind}
      raw={raw} onRaw={setRaw} following={following} onFollowLive={() => setFollowing(true)} />;
  },
};

export const ViewTabs: StoryObj = {
  render: function ViewTabsStory() {
    const [mode, setMode] = useState<RunViewMode>("terminal");
    return <div className="run-live"><RunViewTabs mode={mode} turnCount={4} usage={usage} onSelect={setMode} /></div>;
  },
};

export const ViewTabsWithoutUsage: StoryObj = {
  render: () => <div className="run-live"><RunViewTabs mode="turns" turnCount={0} usage={{}} onSelect={() => undefined} /></div>,
};
