# QuantumAtlas Raw Migration

These one-off scripts migrate `/mnt/team/QuantumAtlas/raw` into the fixed
sharded layout:

```text
raw/pdf/{ym}/{paper_key}.pdf
raw/markdown/{ym}/{paper_key}.md
raw/json/{ym}/{paper_key}.json
raw/images/{ym}/{paper_key}/{original_image_name}.{ext}
raw/index.sqlite
raw/migration-reports/
```

All scripts default to dry-run. Add `--execute` only after the dry-run output
looks right.

## Order

1. Back up and empty raw:

   ```bash
   python tmp/prepare_empty_raw.py
   python tmp/prepare_empty_raw.py --execute
   find /mnt/team/QuantumAtlas/raw -mindepth 1 -print | wc -l
   ```

2. Copy bulk PDFs:

   ```bash
   python tmp/migrate_bulk_pdfs.py
   python tmp/migrate_bulk_pdfs.py --execute
   ```

3. Copy KnowledgeBase markdown, generated JSON, and image directories:

   ```bash
   python tmp/migrate_kb_assets.py
   python tmp/migrate_kb_assets.py --execute
   ```

4. Rebuild the SQLite index:

   ```bash
   python tmp/rebuild_raw_index.py
   python tmp/rebuild_raw_index.py --execute
   ```

## Batch Controls

`migrate_bulk_pdfs.py` and `migrate_kb_assets.py` support:

```bash
--limit N
--offset N
--before '2026-04-29T00:00:00+08:00'
```

`--before` filters by source file mtime. PDF migration uses the PDF mtime; KB
migration uses the latest mtime among the markdown and image files for a paper.

`migrate_kb_assets.py` checks the known bulk layouts first:

```text
/mnt/team/Papercrawl/bulk/{paper_key}.pdf
/mnt/team/Papercrawl/bulk/quant-ph/pdf/{ym}/{paper_key}.pdf
```

Use `--deep-search-bulk` only if you need a slower fallback search under
`bulk/**/*.pdf`.

## Reports

Reports are written under `raw/migration-reports/` during `--execute` runs:

```text
pdf-counts-by-ym.tsv
kb-counts-by-ym.tsv
conflicts.tsv
skipped.tsv
missing-metadata.tsv
```

Conflict checks run before copying. If a target path already exists, or one
batch maps two source files to the same target, the script stops without
overwriting.

## Acceptance Checks

```bash
find /mnt/team/Papercrawl/bulk -type f -name '*.pdf' -print | wc -l
find /mnt/team/QuantumAtlas/raw/pdf -type f -name '*.pdf' -print | wc -l

find /mnt/team/QuantumAtlas/raw/pdf -mindepth 2 -maxdepth 2 -type f -name '*.pdf' \
  | awk -F/ '{print $(NF-1)}' | sort | uniq -c

find /mnt/team/QuantumAtlas/raw/markdown -mindepth 2 -maxdepth 2 -type f -name '*.md' | wc -l
find /mnt/team/QuantumAtlas/raw/json -mindepth 2 -maxdepth 2 -type f -name '*.json' | wc -l
find /mnt/team/QuantumAtlas/raw/images -mindepth 2 -maxdepth 2 -type d | wc -l
```
