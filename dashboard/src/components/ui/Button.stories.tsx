import type { Meta, StoryObj } from "@storybook/react-vite";

import { Button, IconButton } from "./Button";

const meta = {
  title: "UI/Button",
  component: Button,
  args: { children: "Prepare workspace" },
  argTypes: {
    variant: { control: "inline-radio", options: ["default", "primary", "danger"] },
    size: { control: "inline-radio", options: ["default", "compact"] },
  },
} satisfies Meta<typeof Button>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {};
export const Primary: Story = { args: { variant: "primary", children: "Publish version" } };
export const Danger: Story = { args: { variant: "danger", children: "Reject candidate" } };
export const Compact: Story = { args: { size: "compact", children: "Raw" } };
export const WithIcon: Story = { args: { variant: "primary", icon: "create", children: "New work" } };
export const Disabled: Story = { args: { disabled: true, children: "Approve" } };

// Every variant on one canvas: the surface a styling pass actually reviews.
export const AllVariants: Story = {
  render: () => <div style={{ display: "flex", flexWrap: "wrap", gap: 8, alignItems: "center" }}>
    <Button>Default</Button>
    <Button variant="primary">Primary</Button>
    <Button variant="danger">Danger</Button>
    <Button size="compact">Compact</Button>
    <Button variant="primary" size="compact">Compact primary</Button>
    <Button icon="workflow">With icon</Button>
    <Button disabled>Disabled</Button>
    <IconButton icon="settings" label="Open settings" />
    <IconButton icon="x" label="Close" />
  </div>,
};

export const Icons: StoryObj<typeof IconButton> = {
  render: () => <div style={{ display: "flex", gap: 4 }}>
    <IconButton icon="menu" label="Open navigation" />
    <IconButton icon="search" label="Search" />
    <IconButton icon="chevron-down" label="Expand" />
    <IconButton icon="x" label="Dismiss" />
  </div>,
};
