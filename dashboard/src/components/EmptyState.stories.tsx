import type { Meta, StoryObj } from "@storybook/react-vite";

import { Button } from "./ui/Button";
import { EmptyState, type EmptyStateKind } from "./InteractionPatterns";

const meta = {
  title: "Patterns/EmptyState",
  component: EmptyState,
  args: { kind: "empty", title: "No work yet", message: "Create the first piece of work to start a run." },
  argTypes: { kind: { control: "inline-radio", options: ["empty", "filtered", "awaiting", "unavailable"] } },
} satisfies Meta<typeof EmptyState>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Empty: Story = { args: { action: <Button variant="primary" icon="create">New work</Button> } };
export const Filtered: Story = { args: { kind: "filtered", title: "No matches", message: "No work matches the current filter." } };
export const Awaiting: Story = { args: { kind: "awaiting", title: "Awaiting approval", message: "A human decision is required before this run advances." } };
export const Unavailable: Story = { args: { kind: "unavailable", title: "Unavailable", message: "The daemon did not return a projection." } };

export const EveryKind: Story = {
  render: () => <div style={{ display: "grid", gap: 12, gridTemplateColumns: "repeat(auto-fit, minmax(280px, 1fr))" }}>
    {(["empty", "filtered", "awaiting", "unavailable"] as EmptyStateKind[]).map((kind) =>
      <EmptyState key={kind} kind={kind} title={kind} message={`The \`${kind}\` presentation.`} />)}
  </div>,
};
