# Bundled Markdown dependencies

These browser builds are embedded with the dashboard so rendering works offline.

- Marked 15.0.12: https://cdn.jsdelivr.net/npm/marked@15.0.12/marked.min.js (MIT; see marked.LICENSE.md)
- DOMPurify 3.4.15: https://cdn.jsdelivr.net/npm/dompurify@3.4.15/dist/purify.min.js (Apache-2.0 OR MPL-2.0; see purify.LICENSE)

Keep the parser and sanitizer separate. `format.js` treats model HTML as text,
sanitizes generated markup, and restricts links before inserting it into the DOM.
When upgrading, rerun `scripts/test-eval-web.cjs`, including its Markdown safety checks.
