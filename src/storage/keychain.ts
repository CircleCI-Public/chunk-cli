const SERVICE = "com.circleci.cli";

export async function saveSecret(name: string, value: string): Promise<void> {
	await Bun.secrets.set({ service: SERVICE, name, value });
}

export async function loadSecret(name: string): Promise<string | undefined> {
	return (await Bun.secrets.get({ service: SERVICE, name })) ?? undefined;
}

export async function deleteSecret(name: string): Promise<boolean> {
	return Bun.secrets.delete({ service: SERVICE, name });
}
