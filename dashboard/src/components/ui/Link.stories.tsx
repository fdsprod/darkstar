import type { Meta, StoryObj } from "@storybook/react-vite";

import { Link, NavigationAction } from "./Link";

const meta = {
  title: "UI/Link",
  component: Link,
  args: { href: "/work/DS-004", children: "Open work DS-004" },
} satisfies Meta<typeof Link>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {};

// The router-free contract: navigation is a callback, so the story records it
// instead of changing the URL.
export const Navigating: Story = { args: { onNavigate: (href) => window.alert(`navigate to ${href}`) } };

export const AsAction: StoryObj = {
  render: () => <NavigationAction href="/checkpoints">Review pending checkpoints</NavigationAction>,
};
