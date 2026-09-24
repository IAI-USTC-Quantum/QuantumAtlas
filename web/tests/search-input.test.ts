import { describe, expect, it } from 'vitest'
import { parseSearchInput } from '../src/lib/search-input'

const doi = '10.1109/tac.2010.2050710'

describe('exact DOI search input', () => {
  it.each([
    ['10.1109/TAC.2010.2050710', doi],
    ['  10.1109/TAC.2010.2050710  ', doi],
    ['doi:10.1109/TAC.2010.2050710', doi],
    ['DOI: 10.1109/TAC.2010.2050710', doi],
    ['https://doi.org/10.1109/TAC.2010.2050710', doi],
    ['http://dx.doi.org/10.1109/TAC.2010.2050710', doi],
    ['HTTPS://DOI.ORG/10.1109%2FTAC.2010.2050710?utm_source=test#section', doi],
    ['https://dx.doi.org/10.1109%2FTAC.2010.2050710', doi],
    ['10.1002/(SICI)1097-4571(199704)48:4<355::AID-ASI8>3.0.CO;2-R',
      '10.1002/(sici)1097-4571(199704)48:4<355::aid-asi8>3.0.co;2-r'],
    ['10.12345/A.B(C);D/E.', '10.12345/a.b(c);d/e.'],
    ['10.1234/Literal%2FValue', '10.1234/literal%2fvalue'],
    ['https://doi.org/10.1234/Literal%252FValue', '10.1234/literal%2fvalue'],
    ['https://doi.org/10.1234/a%23b%3Fc', '10.1234/a#b?c'],
  ])('recognizes %s without also sending text', (input, expected) => {
    expect(parseSearchInput(input)).toEqual({ doi: expected })
    expect(parseSearchInput(input)).not.toHaveProperty('text')
  })

  it.each([
    '', '  ', 'surface code', '  quantum error correction  ',
    'papers related to 10.1109/tac.2010.2050710',
    '10.1109/tac.2010.2050710 related work',
    '10.1109/tac.2010.2050710\n10.1234/other',
    'doi:', '10.123/x', '10.1234567890/x', '10.1234/',
    '10.1234/a\u0000b',
    'https://publisher.example/10.1234/example',
    'https://doi.org.evil.example/10.1234/example',
    'https://doi.org@evil.example/10.1234/example',
    'https://user:pass@doi.org/10.1234/example',
    'https://doi.org:8600/10.1234/example',
    'ftp://doi.org/10.1234/example',
    'https://doi.org/10.1234/bad%ZZ',
    'https://doi.org/10.1234/with%20space',
    'https://doi.org/10.1234/with%00control',
    'https://doi.org/10.1234/two\nlines',
    'https://doi.org/10.1234/two words',
    'https://doi.org/?doi=10.1234/example',
    'arXiv:2401.12345',
  ])('keeps topics and unsupported/malformed inputs as text: %s', (input) => {
    expect(parseSearchInput(input)).toEqual({ text: input.trim() })
  })
})
