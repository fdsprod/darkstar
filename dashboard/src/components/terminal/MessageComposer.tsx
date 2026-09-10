export interface MessageComposerProps {
  value: string;
  onChange(value: string): void;
  onSend(): void;
  sending: boolean;
  disabled: boolean;
  placeholder: string;
}

// Enter sends; Shift+Enter and IME composition keep their normal meaning.
export function MessageComposer({ value, onChange, onSend, sending, disabled, placeholder }: MessageComposerProps) {
  return <form className="run-message-composer" onSubmit={(event) => { event.preventDefault(); onSend(); }}>
    <label className="sr-only" htmlFor="run-guidance">Message the agent</label>
    <textarea id="run-guidance" value={value} onChange={(event) => onChange(event.target.value)} maxLength={32000}
      disabled={sending || disabled} placeholder={placeholder} rows={2}
      onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); onSend(); } }} />
    <button aria-label="Send message to agent" disabled={sending || disabled || !value.trim()} type="submit">{sending ? "…" : "↑"}</button>
  </form>;
}
