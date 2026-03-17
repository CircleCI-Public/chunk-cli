import { Box, Text, useApp, useInput } from "ink";
import { useState } from "react";

type SelectPromptProps = {
	message: string;
	options: Array<{ label: string; value: string }>;
	onSelect: (value: string) => void;
};

export function SelectPrompt({ message, options, onSelect }: SelectPromptProps) {
	const { exit } = useApp();
	const [focusIndex, setFocusIndex] = useState(0);
	const [selected, setSelected] = useState(false);

	const visibleCount = 10;
	const startIndex = Math.min(
		Math.max(0, focusIndex - Math.floor(visibleCount / 2)),
		Math.max(0, options.length - visibleCount),
	);
	const visibleOptions = options.slice(startIndex, startIndex + visibleCount);

	useInput(
		(_input, key) => {
			if (key.downArrow) {
				setFocusIndex((i) => Math.min(i + 1, options.length - 1));
			}
			if (key.upArrow) {
				setFocusIndex((i) => Math.max(i - 1, 0));
			}
			if (key.return) {
				setSelected(true);
				const option = options[focusIndex];
				if (option) {
					onSelect(option.value);
				}
				exit();
			}
		},
		{ isActive: !selected },
	);

	return (
		<Box flexDirection="column">
			<Text>{message}</Text>
			{!selected &&
				visibleOptions.map((option, i) => {
					const actualIndex = startIndex + i;
					const isFocused = actualIndex === focusIndex;
					return (
						<Box key={option.value}>
							<Text color={isFocused ? "cyan" : undefined}>
								{isFocused ? "❯ " : "  "}
								{option.label}
							</Text>
						</Box>
					);
				})}
		</Box>
	);
}
