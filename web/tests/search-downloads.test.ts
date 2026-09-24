import { describe, expect, it } from 'vitest'
import { downloadChoices, downloadInput } from '../src/lib/search-downloads'
import type { SearchHit } from '../src/lib/api'

const hit = (fields: Partial<SearchHit>): SearchHit => ({ source: 'fixture', score: 0, ...fields })

describe('search downloader identifiers', () => {
  it('rejects titles, catalog IDs, OpenAlex IDs, generic links and unsafe URLs', () => {
    for (const fields of [
      { title: '2401.12345 mentioned in a title' }, { paper_id: 'paper-123' },
      { url: 'https://openalex.org/W123' }, { url: 'https://example.org/paper.pdf' },
      { url: 'javascript:alert(1)' }, { url: 'https://user:secret@doi.org/10.1234/example' },
      { arxiv_id: 'words 2401.12345' },
    ]) expect(downloadInput(hit(fields))).toBeUndefined()
  })

  it('accepts supported identifiers and extracts identities from supported URLs', () => {
    for (const [fields, expected] of [
      [{ arxiv_id: 'arXiv:2401.12345v2' }, '2401.12345v2'],
      [{ doi: 'doi:10.1234/Example' }, '10.1234/Example'],
      [{ url: 'https://export.arxiv.org/pdf/quant-ph/9508027v2.pdf' }, 'quant-ph/9508027v2'],
      [{ url: 'https://www.doi.org/10.1234/example' }, '10.1234/example'],
      [{ url: 'https://publisher.example/doi/10.1234/example' }, '10.1234/example'],
      [{ url: 'https://www.biorxiv.org/content/10.1101/2024.01.01.123456' }, '10.1101/2024.01.01.123456'],
      [{ url: 'https://pmc.ncbi.nlm.nih.gov/articles/PMC123456/' }, 'https://pmc.ncbi.nlm.nih.gov/articles/PMC123456/'],
    ] as const) expect(downloadInput(hit(fields))).toBe(expected)
  })

  it('deduplicates DOI/arxiv versions, resolver URLs, catalog IDs and bridge hits across sources', () => {
    const choices = downloadChoices([
      hit({ arxiv_id: '2401.12345v1' }), hit({ doi: '10.1234/EXAMPLE' }),
      hit({ doi: '10.1234/example', arxiv_id: '2401.12345v2', paper_id: 'catalog-1' }),
      hit({ url: 'https://arxiv.org/abs/2401.12345' }),
      hit({ url: 'https://doi.org/10.1234/example' }),
      hit({ arxiv_id: '2401.99999', paper_id: 'catalog-1' }),
      hit({ paper_id: 'title-only' }),
    ])
    expect(choices).toHaveLength(1)
    expect(choices[0].aliases).toContain('paper:catalog-1')
    expect(choices[0].aliases).toContain('doi:10.1234/example')
    expect(choices[0].input).toBe('2401.12345v1')
  })
})
