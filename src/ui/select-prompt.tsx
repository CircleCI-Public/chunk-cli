import { Select } from "@inkjs/ui";
import { Box, Text, useApp } from "ink";
import { useState } from "react";

type SelectPromptProps = {
	message: string;
	options: Array<{ label: string; value: string }>;
	onSelect: (value: string) => void;
};

export function SelectPrompt({ message, options, onSelect }: SelectPromptProps) {
	const { exit } = useApp();
	const [selected, setSelected] = useState(false);

	return (
		<Box flexDirection="column">
			<Text>{message}</Text>
			{!selected && (
				<Select
					options={options}
					onChange={(value) => {
						setSelected(true);
						onSelect(value);
						exit();
					}}
				/>
			)}
		</Box>
	);
}
