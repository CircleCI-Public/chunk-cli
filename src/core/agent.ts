import Anthropic from "@anthropic-ai/sdk";
import { VALIDATION_MODEL } from "../config";

export async function validateApiKeyWithServer(apiKey: string): Promise<boolean> {
	const client = new Anthropic({ apiKey });

	try {
		await client.messages.countTokens({
			model: VALIDATION_MODEL,
			messages: [{ role: "user", content: "auth test message" }],
		});
		return true;
	} catch (error) {
		if (error instanceof Anthropic.AuthenticationError) {
			return false;
		}
		if (error instanceof Anthropic.RateLimitError) {
			return true;
		}
		return false;
	}
}
