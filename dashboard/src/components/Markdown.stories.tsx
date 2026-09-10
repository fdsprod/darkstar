import type { Meta, StoryObj } from "@storybook/react-vite";

import { Markdown, MarkdownInline } from "./Markdown";
import { CodeBlockView, StructuredCard } from "./document/MarkdownBlocks";
import { sourceLines } from "./document/markdownModel";

const meta = { title: "Document/Markdown", component: Markdown } satisfies Meta<typeof Markdown>;
export default meta;

const prose = [
  "# Rich document review", "",
  "Review **bold text**, *italic text*, `inline code`, and ~~struck text~~ together.", "",
  "## Checklist", "- [x] Preserve history", "- [ ] Human approval", "  - Nested detail", "",
  "1. Prepare workspace", "2. Validate", "3. Deliver", "",
  "> [!IMPORTANT]", "> Runtime validation determines success, not a model's claim.", "",
  "| Name | Status |", "| --- | --- |", "| Plan | Ready |", "| Review | Pending |", "",
  "---", "",
  "```diff plan.md", "- old requirement", "+ new requirement", "#! Preserve the source mapping.", "```",
].join("\n");

export const Prose: StoryObj<typeof meta> = { args: { text: prose } };

export const Inline: StoryObj = {
  render: () => <p><MarkdownInline text="Mixed **bold**, *italic*, `code`, and a [link](https://example.com)." start={0} /></p>,
};

export const Code: StoryObj = {
  render: () => <CodeBlockView info="ts" copied={false} onCopy={() => undefined}
    lines={sourceLines("export function advance(run: Run): Run {\n  return schedule(run);\n}")}
    render={(text, start) => <span data-source-start={start}>{text}</span>} />,
};

export const CodeCopied: StoryObj = {
  render: () => <CodeBlockView info="ts" copied onCopy={() => undefined}
    lines={sourceLines("export const version = 1;")}
    render={(text, start) => <span data-source-start={start}>{text}</span>} />,
};

export const Structured: StoryObj = {
  render: () => <StructuredCard title="Endpoint" values={{
    method: "PATCH",
    path: "/documents/:id",
    summary: "Updates a document.",
    parameters: [{ name: "id", in: "path", type: "string", required: "true" }],
  }} />,
};
