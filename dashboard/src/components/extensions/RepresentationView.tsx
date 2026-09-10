import type { ComponentType } from "react";

export interface RepresentationProps { text: string; mediaType: string }
export interface RepresentationRenderer { mediaType: string; View: ComponentType<RepresentationProps> }

// Only host-registered components can render content. Callers retain revision
// binding, disclosure checks, selection anchors, annotations, and decisions.
export function createRepresentationCatalog(renderers: readonly RepresentationRenderer[]) {
  const catalog = new Map<string, ComponentType<RepresentationProps>>();
  for (const renderer of renderers) {
    const key = renderer.mediaType.toLowerCase();
    if (catalog.has(key)) throw new Error(`Duplicate representation renderer: ${key}`);
    catalog.set(key, renderer.View);
  }
  return function RepresentationView(props: RepresentationProps) {
    const View = catalog.get(props.mediaType.split(";", 1)[0].trim().toLowerCase());
    return View ? <View {...props} /> : <pre tabIndex={0}>{props.text}</pre>;
  };
}

export const RepresentationView = createRepresentationCatalog([]);
