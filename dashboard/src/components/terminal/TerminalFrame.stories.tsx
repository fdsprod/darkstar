import { useRef } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";

import { entries } from "./stories.fixtures";
import { HistoryNotice, ShowMoreHistory, TerminalFrame, TranscriptEmpty, TranscriptScroll } from "./TerminalFrame";
import { TranscriptRow } from "./TranscriptRow";

const meta = {
  title: "Terminal/TerminalFrame",
  component: TerminalFrame,
  parameters: { layout: "fullscreen" },
  args: { title: "implement-ci · agent transcript", state: "● Live", children: null },
} satisfies Meta<typeof TerminalFrame>;

export default meta;

function Scroll({ children }: { children: React.ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  return <TranscriptScroll turns={false} scrollRef={ref} onScroll={() => undefined}>{children}</TranscriptScroll>;
}

export const Live: StoryObj<typeof meta> = {
  render: (args) => <TerminalFrame {...args}>
    <Scroll>{entries.map((entry) => <TranscriptRow key={entry.id} entry={entry} raw={false} />)}</Scroll>
  </TerminalFrame>,
};

export const LoadingHistory: StoryObj<typeof meta> = {
  args: { state: "Loading history…" },
  render: (args) => <TerminalFrame {...args}><Scroll><TranscriptEmpty>Reading the saved transcript…</TranscriptEmpty></Scroll></TerminalFrame>,
};

export const Reconnecting: StoryObj<typeof meta> = {
  args: { state: "Reconnecting…" },
  render: (args) => <TerminalFrame {...args}>
    <HistoryNotice tone="error">The transcript stream dropped. Reconnecting from the last saved event.</HistoryNotice>
    <Scroll>{entries.slice(0, 3).map((entry) => <TranscriptRow key={entry.id} entry={entry} raw={false} />)}</Scroll>
  </TerminalFrame>,
};

// Recoverable history is never silently dropped; the gap is stated.
export const DegradedHistory: StoryObj<typeof meta> = {
  args: { state: "412 events" },
  render: (args) => <TerminalFrame {...args}>
    <HistoryNotice>18 older observations have no recoverable content. Available history is shown below.</HistoryNotice>
    <HistoryNotice>This older run saved 4 tool requests without their responses. New runs save both.</HistoryNotice>
    <Scroll>
      {entries.slice(0, 4).map((entry) => <TranscriptRow key={entry.id} entry={entry} raw={false} />)}
      <ShowMoreHistory hidden={212} onShowMore={() => undefined} />
    </Scroll>
  </TerminalFrame>,
};

export const NoMatches: StoryObj<typeof meta> = {
  args: { state: "0 events" },
  render: (args) => <TerminalFrame {...args}>
    <Scroll><TranscriptEmpty>No events match this view. Adjust the timeline or filters.</TranscriptEmpty></Scroll>
  </TerminalFrame>,
};

export const AwaitingFirstMessage: StoryObj<typeof meta> = {
  args: { state: "● Live" },
  render: (args) => <TerminalFrame {...args}>
    <Scroll><TranscriptEmpty>Waiting for the agent’s first message. Its output and tool calls will appear here.</TranscriptEmpty></Scroll>
  </TerminalFrame>,
};
