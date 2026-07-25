// The tests run in a plain node environment; the only browser API the source
// under test uses is localStorage (session.ts), so provide a minimal
// in-memory implementation instead of pulling in a full DOM. (Node 22+ has a
// built-in localStorage, but it is non-functional unless the process is
// started with --localstorage-file, so it cannot be relied on.)
function memoryStorage(): Storage {
  let store = new Map<string, string>()
  return {
    getItem: (key: string) => store.get(key) ?? null,
    setItem: (key: string, value: string) => void store.set(key, String(value)),
    removeItem: (key: string) => void store.delete(key),
    clear: () => void (store = new Map()),
    key: (i: number) => [...store.keys()][i] ?? null,
    get length() {
      return store.size
    },
  }
}

Object.defineProperty(globalThis, 'localStorage', {
  value: memoryStorage(),
  configurable: true,
  writable: true,
})
