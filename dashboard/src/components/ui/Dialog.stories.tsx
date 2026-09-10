import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";

import { Button } from "./Button";
import { DialogFooter, DialogForm, DialogHeader, ModalDialog } from "./Dialog";
import { FormError, TextField } from "./Field";

const meta = {
  title: "UI/ModalDialog",
  component: ModalDialog,
  parameters: { layout: "fullscreen" },
} satisfies Meta<typeof ModalDialog>;

export default meta;

// A wrapper owns `open`; the dialog itself only reflects it.
function Harness({ busy = false, error = "" }: { busy?: boolean; error?: string }) {
  const [open, setOpen] = useState(true);
  return <div style={{ padding: 24 }}>
    <Button variant="primary" onClick={() => setOpen(true)}>Register project</Button>
    <ModalDialog open={open} busy={busy} labelledBy="register-title" onClose={() => setOpen(false)}>
      <DialogForm busy={busy} onSubmit={(event) => { event.preventDefault(); setOpen(false); }}>
        <DialogHeader id="register-title" title="Register project" intro="The daemon verifies the source before the project accepts work." />
        <TextField label="Project name" required defaultValue="darkstar" />
        <TextField label="Source" required defaultValue="C:\src\darkstar" />
        <FormError>{error}</FormError>
        <DialogFooter note="Drafts are kept until the project is registered.">
          <Button size="compact" disabled={busy} onClick={() => setOpen(false)}>Cancel</Button>
          <Button variant="primary" type="submit" disabled={busy}>{busy ? "Registering…" : "Register"}</Button>
        </DialogFooter>
      </DialogForm>
    </ModalDialog>
  </div>;
}

export const Open: StoryObj = { render: () => <Harness /> };
export const Submitting: StoryObj = { render: () => <Harness busy /> };
export const Rejected: StoryObj = { render: () => <Harness error="That source path is not a Git repository." /> };
