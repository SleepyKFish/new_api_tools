// Mock data must be explicitly enabled so an empty API URL can mean same-origin /api.
export const MOCK_MODE = import.meta.env.VITE_USE_MOCK === 'true'
