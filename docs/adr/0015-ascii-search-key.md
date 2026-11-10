# 0015. ASCII transliterated search keys for bilingual search

- Status: accepted
- Date: 2026-11-10

## Decision
Every product name and alias is normalized into a `search_key`: lower-cased, Bangla transliterated to Latin (so "মিনিকেট" and "miniket" meet), Bangla digits to ASCII, quantities normalized ("5kg" → "5000g"). Trigram similarity runs on that key. Queries go through the same function (`internal/search`, fuzzed).

## Consequences
- One GIN index per table serves both scripts.
- Transliteration is approximate; aliases cover common spellings.
