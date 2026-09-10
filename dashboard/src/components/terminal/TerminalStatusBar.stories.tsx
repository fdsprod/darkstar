import type { Meta, StoryObj } from "@storybook/react-vite";

import { ContextMeter, TerminalStatusBar } from "./TerminalStatusBar";

const meta = {
  title: "Terminal/StatusBar",
  component: TerminalStatusBar,
  parameters: { layout: "fullscreen" },
  decorators: [(Story) => <div className="run-terminal run-terminal--dock"><div className="run-terminal__dock"><Story /></div></div>],
  args: {
    node: "implement-ci",
    scope: "workflow/windows-ci v3",
    provider: "codex",
    model: "gpt-5.6-sol",
    effort: "medium",
    context: { used: 23_256, window: 258_400 },
    hint: "Enter to send · Shift+Enter for a new line",
  },
} satisfies Meta<typeof TerminalStatusBar>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Full: Story = {};
export const Fresh: Story = { args: { context: { used: 4_120, window: 258_400 } } };
export const Pressured: Story = { args: { context: { used: 219_640, window: 258_400 } } };
export const Exhausted: Story = { args: { context: { used: 258_400, window: 258_400 } } };

// A provider that never reports a model, effort, or window still gets a bar.
export const ProviderOnly: Story = { args: { model: undefined, effort: undefined, context: undefined } };
export const WithoutHint: Story = { args: { hint: undefined } };
export const NodeOnly: Story = { args: { scope: undefined, provider: undefined, model: undefined, effort: undefined, context: undefined, hint: undefined } };

export const Meters: StoryObj = {
  render: () => <div style={{ display: "grid", gap: 10, padding: 12 }}>
    {[0, 9, 34, 68, 91, 100].map((percent) =>
      <ContextMeter key={percent} used={Math.round(258_400 * percent / 100)} window={258_400} />)}
  </div>,
};
