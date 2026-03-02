/**
 * Ambient module declarations for `import ... with { type: "text" }`.
 *
 * These cover the template file extensions used by `chunk hook repo init`.
 * Bun resolves the imports at build time; TypeScript needs these declarations
 * to understand the type of the default export.
 *
 * JSON templates are NOT declared here — they use `resolveJsonModule` (imported
 * as objects, then stringified in the manifest).
 */

declare module "*.yml" {
	const content: string;
	export default content;
}

declare module "*.md" {
	const content: string;
	export default content;
}

declare module "*/gitignore" {
	const content: string;
	export default content;
}
