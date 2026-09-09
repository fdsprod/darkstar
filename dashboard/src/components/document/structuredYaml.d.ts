export function parse(source: string): {data: Record<string, unknown>; errors: string[]};
export function mapApiSpec(data: Record<string, unknown>): {props: Record<string, unknown>; errors: string[]};
export function mapDataModel(data: Record<string, unknown>): {props: Record<string, unknown>; errors: string[]};
