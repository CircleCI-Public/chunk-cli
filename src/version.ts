import packageJson from "../package.json";

// __CHUNK_VERSION__ is injected at build time via `bun build --define '__CHUNK_VERSION__="x.y.z"'`
// (GoReleaser passes this automatically via .goreleaser.yaml flags).
// Falls back to package.json version for local dev.
declare const __CHUNK_VERSION__: string | undefined;
export const VERSION =
	typeof __CHUNK_VERSION__ !== "undefined" ? __CHUNK_VERSION__ : packageJson.version;
