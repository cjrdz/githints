<script module lang="ts">
	export const MODULE_VERSION = "1.0.0";
</script>

<script lang="ts">
	import { onMount } from "svelte";
	import { fetchUser } from "../../lib/api";
	import type { User } from "../../types/user";

	interface Props {
		userId: string;
		label?: string;
	}

	let { userId, label = "User" }: Props = $props();

	let user = $state<User | null>(null);

	const displayName = $derived(user ? user.name : label);

	export function refresh(): void {
		load();
	}

	async function load(): Promise<void> {
		user = await fetchUser(userId);
	}

	onMount(() => {
		load();
	});
</script>

<h1>{displayName}</h1>

<style>
	h1 {
		color: navy;
	}
</style>
