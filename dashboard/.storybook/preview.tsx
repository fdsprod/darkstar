import type { Preview } from "@storybook/react-vite";

import "../src/styles.css";

// The dashboard ships a single dark surface; previews inherit the real tokens
// instead of Storybook's default white canvas.
const preview: Preview = {
  parameters: {
    layout: "padded",
    controls: { expanded: true, matchers: { color: /(background|color)$/i, date: /Date$/i } },
    backgrounds: {
      options: {
        page: { name: "Page", value: "#080b12" },
        surface: { name: "Surface", value: "#0d111a" },
        sidebar: { name: "Sidebar", value: "#0a0d14" },
      },
    },
    a11y: { test: "error" },
  },
  initialGlobals: { backgrounds: { value: "page" } },
  decorators: [
    (Story) => <div className="page" style={{ padding: 24 }}><Story /></div>,
  ],
};

export default preview;
