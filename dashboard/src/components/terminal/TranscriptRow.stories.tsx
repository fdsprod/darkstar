import type { Meta, StoryObj } from "@storybook/react-vite";

import { entries } from "./stories.fixtures";
import { TranscriptRow, TurnHeading } from "./TranscriptRow";

const byKind = (kind: string, title?: string) => entries.find((entry) => entry.kind === kind && (!title || entry.title === title))!;

const meta = {
  title: "Terminal/TranscriptRow",
  component: TranscriptRow,
  args: { entry: byKind("message"), raw: false },
  // Rows are monospace on the terminal ground, never the page surface.
  decorators: [(Story) => <div className="run-terminal"><div className="run-transcript-scroll"><Story /></div></div>],
} satisfies Meta<typeof TranscriptRow>;

export default meta;
type Story = StoryObj<typeof meta>;

export const AssistantMessage: Story = {};
export const RawFormat: Story = { args: { raw: true } };
export const UserInput: Story = { args: { entry: byKind("user") } };
export const Reasoning: Story = { args: { entry: byKind("reasoning") } };
export const ToolCall: Story = { args: { entry: byKind("tool", "Terminal") } };
export const ToolFailure: Story = { args: { entry: byKind("tool", "File changes") } };
export const AgentError: Story = { args: { entry: byKind("error") } };
export const Decision: Story = { args: { entry: byKind("decision", "Review needed") } };
export const Lifecycle: Story = { args: { entry: byKind("lifecycle") } };

// Every event kind stacked, which is how the colour system is actually judged.
export const EveryKind: Story = {
  render: () => <>{entries.map((entry) => <TranscriptRow key={entry.id} entry={entry} raw={false} />)}</>,
};

export const TurnGrouped: Story = {
  decorators: [(Story) => <div className="run-terminal"><div className="run-transcript-scroll run-transcript-turns"><Story /></div></div>],
  render: () => <>{entries.slice(2, 6).map((entry, index) => <div key={entry.id}>
    <TurnHeading number={index + 1} time={entry.time} />
    <TranscriptRow entry={entry} raw={false} />
  </div>)}</>,
};
