class ThreadUpdateStore {
	private listeners: (() => void)[] = [];

	// Register a listener that will be called when threads should be refreshed
	subscribe(callback: () => void): () => void {
		this.listeners.push(callback);
		// Return unsubscribe function
		return () => {
			this.listeners = this.listeners.filter((l) => l !== callback);
		};
	}

	// Trigger all listeners to refresh threads
	refresh(): void {
		console.log('[ThreadUpdateStore] refresh() called, notifying', this.listeners.length, 'listeners');
		this.listeners.forEach((listener) => listener());
	}
}

// Single instance
export const threadUpdates = new ThreadUpdateStore();
