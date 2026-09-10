import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";

import { Markdown } from "../Markdown";
import { ContentsTree, DocumentReviewLayout, DocumentToolbar, PanelToggle } from "./DocumentReviewLayout";
import type { Heading } from "./markdownModel";

const meta = { title: "Document/ReviewLayout", parameters: { layout: "fullscreen" } } satisfies Meta;
export default meta;

const source = [
  "# Implementation plan", "",
  "The daemon owns scheduling, gates, retries, and terminal state transitions.", "",
  "## Scope", "- [x] Preserve saved transcripts", "- [ ] Human approval before delivery", "",
  "> [!WARNING]", "> Publishing remains human-only.", "",
  "| Stage | Owner |", "| --- | --- |", "| Validation | Runtime |", "| Approval | Human |", "",
  "```ts", "export function advance(run: Run): Run {", "  return schedule(run);", "}", "```",
].join("\n");

const headings: Heading[] = [
  { id: "md-0", level: 1, text: "Implementation plan", start: 0 },
  { id: "md-90", level: 2, text: "Scope", start: 90 },
];

// The layout is fully controlled: panel state lives in the story, the way it
// lives in ArtifactAnnotations in the real application.
function Harness({ startContents = true, startAnnotations = true }) {
  const [format, setFormat] = useState<"formatted" | "raw">("formatted");
  const [contents, setContents] = useState(startContents);
  const [annotations, setAnnotations] = useState(startAnnotations);
  return <DocumentReviewLayout
    toolbar={<DocumentToolbar format={format} onFormat={setFormat} contentsOpen={contents} annotationsOpen={annotations}
      onContents={() => setContents(!contents)} onAnnotations={() => setAnnotations(!annotations)} />}
    contents={contents ? <ContentsTree headings={headings} onSelect={() => undefined} /> : undefined}
    document={format === "raw" ? <pre>{source}</pre> : <Markdown text={source} />}
    annotations={annotations ? <p style={{ padding: 12 }}>Select text in the document to annotate it.</p> : undefined}
  />;
}

export const Default: StoryObj = { render: () => <Harness /> };
export const DocumentOnly: StoryObj = { render: () => <Harness startContents={false} startAnnotations={false} /> };

export const Contents: StoryObj = {
  render: () => <div style={{ maxWidth: 220 }}>
    <ContentsTree headings={headings} onSelect={() => undefined} />
  </div>,
};

export const ContentsEmpty: StoryObj = {
  render: () => <div style={{ maxWidth: 220 }}><ContentsTree headings={[]} onSelect={() => undefined} /></div>,
};

export const Toggles: StoryObj = {
  render: () => <div style={{ display: "flex", gap: 8 }}>
    <PanelToggle label="Contents" open onToggle={() => undefined} />
    <PanelToggle label="Annotations" open={false} onToggle={() => undefined} />
  </div>,
};
