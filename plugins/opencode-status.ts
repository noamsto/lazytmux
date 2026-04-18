import type { Plugin } from "@opencode-ai/plugin";

// OpenCode plugin: bridges lifecycle events to lazytmux's claude-status-update.
// Install: symlink/copy to ~/.config/opencode/plugin/ or .opencode/plugin/
// Requires: claude-status-update on PATH (provided by the lazytmux tmux wrapper)

export const LazytmuxStatus: Plugin = async ({ $ }) => {
	const update = async (state: string) => {
		try {
			await $`claude-status-update ${state}`.quiet().nothrow();
		} catch {
			// Not in tmux or binary missing
		}
	};

	let lastState = "";

	const set = async (state: string) => {
		if (state === lastState) return;
		lastState = state;
		await update(state);
	};

	return {
		event: async ({ event }) => {
			switch (event.type) {
				case "session.created":
					await update("cleanup");
					await set("idle");
					break;

				case "session.deleted":
					await set("clear");
					lastState = "";
					break;

				case "session.error":
					await set("error");
					break;

				case "session.idle":
					await set("done");
					break;

				case "session.compacted":
					await set("processing");
					break;

				case "tool.execute.before":
				case "tool.execute.after":
					await set("processing");
					break;

				case "permission.asked":
					await set("waiting");
					break;

				case "permission.replied":
					await set("processing");
					break;

				case "message.updated":
					await set("processing");
					break;
			}
		},
	};
};
