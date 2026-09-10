export interface GeneralAnnotation { id: string; comment: string }

export function GeneralAnnotations({ annotations, readOnly, onChange }: {
  annotations: readonly GeneralAnnotation[];
  readOnly: boolean;
  onChange(annotations: GeneralAnnotation[]): void;
}) {
  return <ol className="annotation-list" aria-label="General annotation comments">{annotations.map((item, index) =>
    <li key={item.id}><article className="annotation-card" aria-label={`General annotation ${index + 1}`}>
      <div className="annotation-card-body"><strong>General annotation {index + 1}</strong>
        {readOnly ? <p>{item.comment}</p> : <textarea aria-label={`Edit general annotation ${index + 1}`} value={item.comment}
          onChange={event => onChange(annotations.map(note => note.id === item.id ? { ...note, comment: event.target.value } : note))} />}
      </div>
      {!readOnly && <button type="button" className="annotation-remove" aria-label={`Remove general annotation ${index + 1}`}
        onClick={() => onChange(annotations.filter(note => note.id !== item.id))}>×</button>}
    </article></li>
  )}</ol>;
}
