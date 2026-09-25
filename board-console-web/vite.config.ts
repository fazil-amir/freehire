import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The dev server's origin (http://localhost:5173) is Board Console's default
// allowed CORS origin, so the app calls the API directly — see .env.example.
export default defineConfig({
  plugins: [react()],
  server: { port: 5173, strictPort: true },
});
