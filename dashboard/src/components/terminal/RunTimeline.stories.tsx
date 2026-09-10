import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";

import { RunTimeline } from "./RunTimeline";
import { end, entries, start } from "./stories.fixtures";
import { selectTimelineRange, timelineRange, type TimelineSelection } from "./timelineModel";

const meta = {
  title: "Terminal/RunTimeline",
  component: RunTimeline,
  parameters: { layout: "fullscreen" },
  decorators: [(Story) => <div className="run-terminal"><Story /></div>],
  args: { entries, start, end, range: null, onChange: () => undefined },
} satisfies Meta<typeof RunTimeline>;

export default meta;
type Story = StoryObj<typeof meta>;

export const EntireRun: Story = {};
export const NarrowedRange: Story = { args: { range: [start + 40_000, start + 130_000] } };
export const Empty: Story = { args: { entries: [] } };

// Brushing is live here, driven through the same model the page uses, so the
// interaction can be exercised without a daemon.
export const Brushable: Story = {
  render: function BrushableStory(args) {
    const [selection, setSelection] = useState<TimelineSelection>({ kind: "all" });
    const range = timelineRange(selection, end);
    return <>
      <RunTimeline {...args} range={range} onChange={(next) => setSelection(selectTimelineRange(next, start, end))} />
      <p style={{ padding: "8px 20px", color: "#8b929d", font: "12px ui-monospace, Consolas, monospace" }}>
        selection: {JSON.stringify(selection)}
      </p>
    </>;
  },
};
