Add shareable search to Baby Hippo Gram.

- Put a visibly labelled search input above the gallery.
- Initialize its value from the URL's `q` query parameter.
- Filter posts by title or photographer/author, case-insensitively and ignoring
  whitespace around the user's query. An empty query shows every post.
- As the user types, update only `q` with `history.replaceState`, preserving all
  other query parameters and the URL hash. Remove `q` when the query is empty.
- Respond to browser back/forward navigation (`popstate`) by updating both the
  input and visible results.
- When there are no matches, show `No hippos found.` in an `aria-live` status.
- Add focused user-facing tests for filtering by both fields, case/whitespace,
  URL preservation and clearing, `popstate`, and the empty state.

Keep the existing visual style and add no dependencies.
