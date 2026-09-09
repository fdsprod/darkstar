# Rich document review components

The review defaults to formatted Markdown. Contents and Annotations collapse independently; Raw exposes the original source. The review keeps annotations bound to the exact immutable representation, and only a human decision approves a candidate. Agent revisions continue through the existing versioned review service.

`src/components/document/DocumentReviewLayout.tsx` exports the controlled layout, toolbar, panel toggle, and contents tree. `AnnotationParts.tsx` exports comment cards, the selection composer, panel, and approval actions. `MarkdownBlocks.tsx` contains code, file tree, and structured card views; clipboard and diagram effects have separate wrappers. These components receive data and callbacks, without API or router dependencies.

`ArtifactAnnotations` owns local selection and annotation state. `ArtifactReviewWorkspace` owns API reads, exact-version bindings, revision submission, and decisions. `markdownModel.ts` keeps original source offsets (including CRLF and Unicode); rendered text leaves carry those offsets so repeated phrases never require ambiguous text matching. Structured cards and diagrams expose their source for exact range annotations.

The parser supports headings, inline formatting, lists/tasks, tables, blockquotes/alerts, and code fences. Specialized fences are `apispec`, `datamodel`, `tree`, `diff`, and `mermaid`. Invalid structured fences show diagnostics and their unchanged source. Raw HTML is inert; diagrams use a local strict renderer and display through images.

With the Vite development server running, open `/fixtures/rich-document.html` for a standalone component showcase without a daemon or model. This provides fixtures for future Storybook integration. Run `node --experimental-strip-types --test` in dashboard for model tests and the repository Playwright command with `dashboard/e2e/rich-document.spec.ts` for component interaction tests.

The generic authoring specification is `skills/builtin/rich-artifacts/SKILL.md`. `node scripts/builtin-skills.mjs generate` updates its manifest and embedded runtime copy; `check` detects drift. Markdown-producing execution and revision attempts receive its complete content automatically, without workflow graph or scheduler state. Existing saved prompts are unchanged.
