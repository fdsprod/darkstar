import type { Meta, StoryObj } from "@storybook/react-vite";

import { Field, FormError, SelectField, TextAreaField, TextField } from "./Field";

const meta = {
  title: "UI/Field",
  component: Field,
  args: { label: "Source name", children: <input defaultValue="plan.md" /> },
} satisfies Meta<typeof Field>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {};
export const WithHint: Story = { args: { label: "Roles", hint: "comma separated" } };

export const EveryControl: StoryObj = {
  render: () => <form style={{ maxWidth: 460 }}>
    <TextField label="Source name" required defaultValue="implementation-plan.md" />
    <TextField label="Roles" hint="comma separated" defaultValue="reviewer, approver" />
    <SelectField label="Sensitivity" defaultValue="internal">
      <option value="unknown">Unknown</option>
      <option value="public">Public</option>
      <option value="internal">Internal</option>
    </SelectField>
    <TextAreaField label="Evidence content" rows={5} placeholder="Paste notes, a transcript, or structured evidence…" />
    <FormError>The media type is not accepted for this artifact kind.</FormError>
  </form>,
};

export const Invalid: StoryObj = {
  render: () => <form style={{ maxWidth: 460 }}>
    <TextField label="Media type" defaultValue="application/unknown" aria-invalid />
    <FormError>Provide a media type the artifact contract accepts.</FormError>
  </form>,
};
