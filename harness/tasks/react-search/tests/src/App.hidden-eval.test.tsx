import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import App from './App';
import posts from './data/posts.json';

const NO_RESULTS = 'No hippos found.';

const imagesFor = (predicate: (post: (typeof posts)[number]) => boolean) =>
  posts.filter(predicate).map((post) => post.image);

const allImages = imagesFor(() => true);

// Post images currently exposed to users (hidden elements are excluded by getAllByRole).
const visibleImages = () =>
  screen
    .queryAllByRole('img')
    .map((img) => img.getAttribute('src') ?? '')
    .filter((src) => allImages.includes(src));

const searchInput = (): HTMLInputElement => {
  const inputs = [
    ...screen.queryAllByRole('searchbox', { name: /\S/ }),
    ...screen.queryAllByRole('textbox', { name: /\S/ }),
  ];
  expect(inputs).toHaveLength(1);
  return inputs[0] as HTMLInputElement;
};

const typeQuery = (value: string) => {
  fireEvent.change(searchInput(), { target: { value } });
};

// Result order is left to the implementation, so compare as sets.
const expectResults = async (expected: string[]) => {
  await waitFor(() => {
    const visible = visibleImages();
    expect(visible).toHaveLength(expected.length);
    expect(new Set(visible)).toEqual(new Set(expected));
  });
};

const params = () => new URLSearchParams(window.location.search);

const visitUrl = (url: string) => {
  window.history.replaceState(null, '', url);
};

beforeEach(() => {
  visitUrl('/');
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  visitUrl('/');
});

it('shows a visibly labelled search input above the gallery', () => {
  render(<App />);
  const input = searchInput();

  const labelText = [
    ...Array.from(input.labels ?? []),
    ...(input.getAttribute('aria-labelledby') ?? '')
      .split(/\s+/)
      .filter(Boolean)
      .map((id) => document.getElementById(id)),
  ]
    .map((el) => el?.textContent?.trim() ?? '')
    .join('');
  expect(labelText).not.toBe('');

  const firstImage = screen.getAllByRole('img')[0];
  expect(input.compareDocumentPosition(firstImage) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
});

it('shows every post when the query is empty', async () => {
  render(<App />);
  expect(searchInput().value).toBe('');
  await expectResults(allImages);
  expect(screen.queryByText(NO_RESULTS)).toBeNull();
});

it('filters posts by title', async () => {
  render(<App />);
  typeQuery('mum');
  await expectResults(imagesFor((post) => post.title.toLowerCase().includes('mum')));
  expect(visibleImages()).toHaveLength(3);
});

it('filters posts by photographer', async () => {
  render(<App />);
  typeQuery('Frank Wouters');
  await expectResults(imagesFor((post) => post.author === 'Frank Wouters'));
  expect(visibleImages()).toHaveLength(2);
});

it('matches case-insensitively and ignores surrounding whitespace', async () => {
  render(<App />);
  typeQuery('  SPLASH tIME  ');
  await expectResults(imagesFor((post) => post.title === 'Splash time'));
  expect(visibleImages()).toHaveLength(1);

  typeQuery('\tsAcKtOn ');
  await expectResults(imagesFor((post) => post.author === 'Tim Sackton'));
});

it('initializes the query from the q URL parameter', async () => {
  visitUrl('/?q=wouters');
  render(<App />);
  expect(searchInput().value).toBe('wouters');
  await expectResults(imagesFor((post) => post.author === 'Frank Wouters'));
});

it('updates only q with replaceState, preserving other parameters and the hash', async () => {
  visitUrl('/?sort=new&page=2#gallery');
  const replaceState = vi.spyOn(window.history, 'replaceState');
  const pushState = vi.spyOn(window.history, 'pushState');
  const historyLength = window.history.length;
  render(<App />);

  typeQuery('splash');

  await waitFor(() => expect(params().get('q')).toBe('splash'));
  expect(replaceState).toHaveBeenCalled();
  expect(pushState).not.toHaveBeenCalled();
  expect(window.history.length).toBe(historyLength);
  expect(window.location.pathname).toBe('/');
  expect(window.location.hash).toBe('#gallery');
  expect(new Set(params().keys())).toEqual(new Set(['page', 'q', 'sort']));
  expect(params().get('sort')).toBe('new');
  expect(params().get('page')).toBe('2');
  await expectResults(imagesFor((post) => post.title === 'Splash time'));
});

it('removes q from the URL when the query is cleared', async () => {
  visitUrl('/?sort=new&q=splash#gallery');
  render(<App />);
  await expectResults(imagesFor((post) => post.title === 'Splash time'));

  typeQuery('');

  await waitFor(() => expect(params().has('q')).toBe(false));
  expect(params().get('sort')).toBe('new');
  expect([...params().keys()]).toEqual(['sort']);
  expect(window.location.hash).toBe('#gallery');
  expect(searchInput().value).toBe('');
  await expectResults(allImages);
});

it('updates the input and results on popstate', async () => {
  visitUrl('/?q=splash');
  render(<App />);
  await expectResults(imagesFor((post) => post.title === 'Splash time'));

  act(() => {
    window.history.pushState(null, '', '/?q=Sackton');
    window.dispatchEvent(new PopStateEvent('popstate', { state: null }));
  });
  await waitFor(() => expect(searchInput().value).toBe('Sackton'));
  await expectResults(imagesFor((post) => post.author === 'Tim Sackton'));

  act(() => {
    window.history.pushState(null, '', '/');
    window.dispatchEvent(new PopStateEvent('popstate', { state: null }));
  });
  await waitFor(() => expect(searchInput().value).toBe(''));
  await expectResults(allImages);
});

it('announces when no posts match in an aria-live status', async () => {
  render(<App />);
  typeQuery('zebra');

  await expectResults([]);
  const message = await screen.findByText(NO_RESULTS);
  expect(message.closest('[aria-live]:not([aria-live="off"]), [role="status"]')).not.toBeNull();
});
