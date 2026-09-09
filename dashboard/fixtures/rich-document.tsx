import React,{useState} from 'react';
import {createRoot} from 'react-dom/client';
import {ArtifactAnnotations} from '../src/pages/ArtifactAnnotations';
import type {FeedbackAnnotationDraft} from '../src/pages/artifactReviewModel';
import '../src/styles.css';
const source = [
 '# Component showcase', '', '😀 Review **bold text** and *italic text* together.', '',
 '## Checklist', '- [x] Preserve history', '- [ ] Human approval', '  - Nested item', '',
 '> [!WARNING]', '> This is a **review** prerequisite.', '',
 '| Name | Status |', '| --- | --- |', '| Plan | Ready |', '',
 '```apispec', 'method: PATCH', 'path: /documents/:id', 'summary: Updates a document.', 'parameters:', '  - { name: id, in: path, type: string, required: true }', 'responses:', '  200: Updated', '```', '',
 '```datamodel', 'name: Document', 'fields:', '  - { name: id, type: string, required: true }', '```', '',
 '```datamodel', 'unexpected: invalid', '```', '',
 '```tree', 'src/', '  reader.tsx [modified]', '```', '',
 '```diff reader.tsx', '- old', '+ new', '#! Preserve the source mapping.', '```', '',
 '```mermaid', 'flowchart LR', 'A[Draft] --> B[Human review]', 'B --> C[Approved]', '```', '',
 '<script>alert("unsafe")</script>', '[unsafe](javascript:alert)',
].join('\r\n');
function Fixture(){const [annotations,setAnnotations]=useState<FeedbackAnnotationDraft[]>([]);return <main style={{padding:24}}><ArtifactAnnotations text={source} annotations={annotations} readOnly={false} onChange={setAnnotations}/><output aria-label="Saved anchors">{JSON.stringify(annotations)}</output></main>;}
createRoot(document.getElementById('root')!).render(<Fixture/>);
