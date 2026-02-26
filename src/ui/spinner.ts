/**
 * Lightweight ANSI-based terminal spinner for showing activity during long operations.
 *
 * Uses braille dot frames matching the @inkjs/ui Spinner dots style for visual consistency.
 * No-op when stdout is not a TTY (piped output).
 */

import { dim } from "./colors";

const FRAMES = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];
const INTERVAL_MS = 80;
const ELAPSED_THRESHOLD_MS = 10_000;

export class TerminalSpinner {
	private frameIndex = 0;
	private timer: ReturnType<typeof setInterval> | null = null;
	private message = "";
	private startTime = 0;
	private isTTY: boolean;

	constructor() {
		this.isTTY = process.stdout.isTTY === true;
	}

	start(message = "Analyzing changes..."): void {
		if (!this.isTTY) return;
		this.message = message;
		this.frameIndex = 0;
		this.startTime = Date.now();
		this.render();
		this.timer = setInterval(() => this.render(), INTERVAL_MS);
	}

	update(message: string): void {
		if (!this.isTTY) return;
		this.message = message;
	}

	stop(): void {
		if (this.timer) {
			clearInterval(this.timer);
			this.timer = null;
		}
		if (this.isTTY) {
			process.stdout.write("\r\x1b[2K");
		}
	}

	stopWithMessage(message: string): void {
		this.stop();
		if (this.isTTY) {
			process.stdout.write(`${message}\n`);
		} else {
			console.log(message);
		}
	}

	/**
	 * Print a message on its own line while keeping the spinner running.
	 * Clears the spinner line, prints the message, then re-renders the spinner below it.
	 */
	log(message: string): void {
		if (!this.isTTY) {
			console.log(message);
			return;
		}
		process.stdout.write("\r\x1b[2K");
		process.stdout.write(`${message}\n`);
		this.render();
	}

	private render(): void {
		const frame = FRAMES[this.frameIndex % FRAMES.length];
		this.frameIndex++;

		let line = `${frame} ${this.message}`;

		const elapsed = Date.now() - this.startTime;
		if (elapsed >= ELAPSED_THRESHOLD_MS) {
			const seconds = Math.round(elapsed / 1000);
			line += dim(` (${seconds}s)`);
		}

		process.stdout.write(`\r\x1b[2K${line}`);
	}
}

/**
 * Truncate a string to a maximum length, adding ellipsis if needed.
 */
export function truncate(text: string, maxLength: number): string {
	if (text.length <= maxLength) return text;
	return `${text.slice(0, maxLength - 1)}…`;
}
