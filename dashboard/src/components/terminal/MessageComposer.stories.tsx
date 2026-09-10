import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";

import { MessageComposer } from "./MessageComposer";

const running = "Interject or give the agent guidance…";

const meta = {
  title: "Terminal/MessageComposer",
  component: MessageComposer,
  args: { value: "", sending: false, disabled: false, placeholder: running, onChange: () => undefined, onSend: () => undefined },
} satisfies Meta<typeof MessageComposer>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Empty: Story = {};
export const Composed: Story = { args: { value: "Reuse the existing Assert-Toolchain step rather than adding a second one." } };
export const Sending: Story = { args: { value: "Reuse the existing step.", sending: true } };
export const WaitingOnReview: Story = { args: { disabled: true, placeholder: "Resolve the question or review above to continue." } };
export const RunFinished: Story = { args: { disabled: true, placeholder: "Messages are available while an agent is running." } };

// Enter sends and Shift+Enter inserts a newline; the story lets both be tried.
export const Interactive: Story = {
  render: function InteractiveStory(args) {
    const [value, setValue] = useState("");
    const [sent, setSent] = useState<string[]>([]);
    return <>
      <MessageComposer {...args} value={value} onChange={setValue} onSend={() => { if (value.trim()) { setSent([...sent, value]); setValue(""); } }} />
      <ol style={{ color: "#8b929d", font: "12px ui-monospace, Consolas, monospace" }}>{sent.map((item, index) => <li key={index}>{item}</li>)}</ol>
    </>;
  },
};
