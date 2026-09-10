import type { Meta, StoryObj } from "@storybook/react-vite";

import { Button } from "./ui/Button";
import { ActionBar, ActionGuidance, AsyncPanel, DiagnosticsDetails, EmptyState, SectionHeader, StatusBadge, type AsyncState, type EmptyStateKind } from "./InteractionPatterns";

const meta = {
  title: "Patterns/AsyncPanel",
  component: AsyncPanel,
  args: { state: "loading", title: "Synchronizing", message: "Authoritative projections are refreshing." },
  argTypes: { state: { control: "select", options: ["loading", "success", "error", "stale", "cancelled", "validation"] } },
} satisfies Meta<typeof AsyncPanel>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Loading: Story = {};
export const Success: Story = { args: { state: "success", title: "Synchronized", message: "The board matches the daemon." } };
export const Error: Story = { args: { state: "error", title: "Unavailable", message: "Authoritative dashboard data is temporarily unavailable.", action: <Button size="compact">Retry</Button> } };
export const Stale: Story = { args: { state: "stale", title: "Live updates interrupted", message: "Reconnecting to the event stream." } };
export const Compact: Story = { args: { compact: true, state: "validation", title: "Validation failed", message: "Two nodes have unbound inputs." } };

// The full state machine on one canvas, which is what a styling pass reviews.
export const EveryState: Story = {
  render: () => <div style={{ display: "grid", gap: 12 }}>
    {(["loading", "success", "error", "stale", "cancelled", "validation"] as AsyncState[]).map((state) =>
      <AsyncPanel key={state} state={state} title={state} message={`The \`${state}\` presentation.`} />)}
  </div>,
};
