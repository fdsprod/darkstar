import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";

import { AnnotationBadge, AnnotationCard, AnnotationPanel, AnnotationPopup, ApprovalActions } from "./AnnotationParts";
import { GeneralAnnotations, type GeneralAnnotation } from "./GeneralAnnotations";

const meta = { title: "Document/Annotations" } satisfies Meta;
export default meta;

const quote = "The daemon owns scheduling, gates, retries, and terminal state transitions.";

export const Popup: StoryObj = {
  render: function PopupStory() {
    const [comment, setComment] = useState("Clarify which component owns retries.");
    return <div style={{ position: "relative", minHeight: 260 }}>
      <AnnotationPopup left={0} top={0} quote={quote} comment={comment} error="" busy={false} editing={false}
        onComment={setComment} onSave={() => undefined} onCancel={() => undefined} />
    </div>;
  },
};

export const PopupSubmitting: StoryObj = {
  render: () => <div style={{ position: "relative", minHeight: 260 }}>
    <AnnotationPopup left={0} top={0} quote={quote} comment="Clarify ownership." error="" busy editing={false}
      onComment={() => undefined} onSave={() => undefined} onCancel={() => undefined} />
  </div>,
};

export const PopupRejected: StoryObj = {
  render: () => <div style={{ position: "relative", minHeight: 260 }}>
    <AnnotationPopup left={0} top={0} quote={quote} comment="Clarify ownership." error="The candidate changed; reopen the review." busy={false} editing
      onComment={() => undefined} onSave={() => undefined} onCancel={() => undefined} />
  </div>,
};

export const Cards: StoryObj = {
  render: () => <div style={{ display: "grid", gap: 8, maxWidth: 320 }}>
    <AnnotationCard number={1} quote={quote} comment="Clarify which component owns retries." label={quote}
      readOnly={false} active onActivate={() => undefined} onEdit={() => undefined} onRemove={() => undefined} />
    <AnnotationCard number={2} quote="Human approval cannot be replaced by model text." comment="Agreed; cite the approval contract." label="approval"
      readOnly={false} active={false} onActivate={() => undefined} onEdit={() => undefined} onRemove={() => undefined} />
    <AnnotationCard number={3} quote="Runtime validation determines success." comment="Locked after submission." label="validation"
      readOnly active={false} onActivate={() => undefined} onEdit={() => undefined} onRemove={() => undefined} />
    <p>Inline marker: <AnnotationBadge number={1} onActivate={() => undefined} /></p>
  </div>,
};

export const Panel: StoryObj = {
  render: function PanelStory() {
    const [general, setGeneral] = useState<GeneralAnnotation[]>([{ id: "a", comment: "Tighten the summary before approval." }]);
    return <aside className="document-annotations" style={{ maxWidth: 320 }}>
      <AnnotationPanel count={2}
        composer={<GeneralAnnotations annotations={general} readOnly={false} onChange={setGeneral} />}
        actions={<ApprovalActions approveDisabled={false} reviseDisabled={false} rejectDisabled={false} busy="" revisionLabel="Request revisions"
          onApprove={() => undefined} onRevise={() => undefined} onReject={() => undefined} />}>
        <AnnotationCard number={1} quote={quote} comment="Clarify which component owns retries." label={quote}
          readOnly={false} active={false} onActivate={() => undefined} onEdit={() => undefined} onRemove={() => undefined} />
        <AnnotationCard number={2} quote="Human approval cannot be replaced by model text." comment="Cite the approval contract." label="approval"
          readOnly={false} active={false} onActivate={() => undefined} onEdit={() => undefined} onRemove={() => undefined} />
      </AnnotationPanel>
    </aside>;
  },
};

export const PanelEmpty: StoryObj = {
  render: () => <aside className="document-annotations" style={{ maxWidth: 320 }}>
    <AnnotationPanel count={0} composer={<GeneralAnnotations annotations={[]} readOnly={false} onChange={() => undefined} />}>{null}</AnnotationPanel>
  </aside>,
};

export const Decisions: StoryObj = {
  render: () => <div style={{ display: "grid", gap: 16 }}>
    <ApprovalActions approveDisabled={false} reviseDisabled={false} rejectDisabled={false} busy="" revisionLabel="Request revisions"
      onApprove={() => undefined} onRevise={() => undefined} onReject={() => undefined} />
    <ApprovalActions approveDisabled reviseDisabled rejectDisabled busy="approve" revisionLabel="Request revisions"
      onApprove={() => undefined} onRevise={() => undefined} onReject={() => undefined} />
    <ApprovalActions approveDisabled reviseDisabled={false} rejectDisabled busy="request_revisions" revisionLabel="Request revisions"
      onApprove={() => undefined} onRevise={() => undefined} onReject={() => undefined} />
  </div>,
};
