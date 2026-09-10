import type { StorybookConfig } from "@storybook/react-vite";

// Stories live beside the component they document, so a pure component and its
// isolated preview move together.
const config: StorybookConfig = {
  framework: { name: "@storybook/react-vite", options: {} },
  stories: ["../src/**/*.stories.@(ts|tsx)"],
  // addon-mcp serves the component catalog to AI agents over MCP at /mcp
  // while the dev server is running.
  addons: ["@storybook/addon-docs", "@storybook/addon-a11y", "@storybook/addon-mcp"],
  core: { disableTelemetry: true },
  // TypeScript 7 replaced the compiler API that react-docgen-typescript reads,
  // so props tables come from the Babel-based docgen instead.
  typescript: { reactDocgen: "react-docgen" },
};

export default config;
