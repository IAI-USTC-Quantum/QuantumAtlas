// Type declarations for the mock-fixture generator (plain ESM script;
// tests/reader-realpaper.test.ts imports its pure transform).
export type GeneratorProvenance = {
  variant: string
  raw_middle_json_sha256: string
  source_pdf_sha256: string
}

export type GeneratorBlock = {
  page_idx: number
  index: number
  type: string
  content: string
  bbox: number[] | null
}

export type GeneratorFixture = {
  _provenance: GeneratorProvenance
  schema: string
  schema_version: string
  producer: { name: string; version: string } | null
  pages: number
  blocks: GeneratorBlock[]
}

export declare function flattenContent(node: unknown): string
export declare function transformMiddle(
  raw: unknown,
  provenance: GeneratorProvenance,
): GeneratorFixture
