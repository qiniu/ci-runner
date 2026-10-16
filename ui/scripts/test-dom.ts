import { Window } from "happy-dom"

// Radix selects its browser layout effects at module load time. Establish a DOM
// before test imports; individual suites can still install their own windows.
const testWindow = new Window({ url: "http://localhost/" })
Object.defineProperty(globalThis, "window", { configurable: true, writable: true, value: testWindow })
Object.defineProperty(globalThis, "document", { configurable: true, writable: true, value: testWindow.document })
